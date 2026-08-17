//go:build !windows

package acp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// leakingAgentScript emulates codex-acp's double-fork leak: it backgrounds a
// worker WITHOUT setsid (so the worker stays in the SAME process group as
// this script, exactly like the real leak — it is not a full daemonize into
// a new session), writes the worker's pid to $1 for the test to observe, and
// then blocks so the harness's stdin/stdout pipes stay open like a real
// agent's would.
const leakingAgentScript = `#!/bin/sh
( sleep 100 & echo $! > "$1" )
sleep 100
`

// processAlive reports whether pid denotes a process that is still actually
// RUNNING — kill(2)'s signal-0 existence check (sends no signal, just probes
// existence/permission), refined to NOT count a zombie as alive.
//
// That refinement matters here specifically: killProcessGroup SIGKILLs the
// whole group in one broadcast, so the immediate child (/bin/sh) and the
// detached worker die in the same instant — but the worker's real parent WAS
// that now-dead child, not this test process, so on exit the worker
// reparents to the container's PID 1 (or whatever subreaper sits above it).
// Nothing obliges that ancestor to ever call wait() on a PID it didn't
// itself spawn; in a bare container (`just test`'s devcontainer, no init
// process) it typically never does, so the worker sits as an UNREAPED
// ZOMBIE forever — and kill(pid, 0) reports a zombie's PID-table slot as
// "exists" exactly like a running process, making the naive check hang
// until require.Eventually's deadline regardless of how long it waits. The
// worker is nonetheless genuinely dead the instant SIGKILL lands: a zombie
// does no work and holds no CPU/memory beyond that slot. Reaping-ancestor
// bookkeeping is not this package's contract (spawnTransport's close()
// promise is "the worker stops running", not "something eventually calls
// wait(2) on it") — isZombie's /proc read makes that distinction directly
// instead of proxying it through an ancestor's unrelated reaping behavior.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil {
		return errors.Is(err, syscall.EPERM)
	}
	return !isZombie(pid)
}

// isZombie reports whether pid is a Linux zombie (state 'Z' in
// /proc/<pid>/stat — see proc(5)). Fault-tolerant by design: no /proc entry
// (already reaped, or a non-Linux !windows target like Darwin, where a
// zombie's real parent is near-universally a proper reaper like launchd)
// answers false, letting processAlive fall back to kill(2)'s verdict alone.
// The comm field can itself contain parens, so state is read as the token
// right after the LAST ')', never the first.
func isZombie(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	if i := strings.LastIndexByte(string(data), ')'); i >= 0 && i+2 < len(data) {
		return data[i+2] == 'Z'
	}
	return false
}

// waitForFile polls for path to exist and be non-empty, up to timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
	return ""
}

// TestSpawnTransport_CloseReapsDetachedWorker is the regression
// test: spawnTransport's close must kill NOT ONLY the immediate child but
// also a worker that child backgrounds without setsid — a plain
// cmd.Process.Kill() (the pre-fix behavior) never reaches that worker,
// leaking it for the lifetime of the host. This reproduces codex-acp's
// documented double-fork shape with a throwaway shell script rather than a
// real codex-acp binary, so it runs hermetically in CI.
func TestSpawnTransport_CloseReapsDetachedWorker(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "leaking-agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(leakingAgentScript), 0o755))
	pidFile := filepath.Join(dir, "worker.pid")

	// Grace period set SHORT and explicit: this test's own concern (does
	// close() reap a detached worker) is orthogonal to the async-flush
	// grace period, and leakingAgentScript never reacts to stdin closing at
	// all — the default grace would always fully elapse here, needlessly
	// slowing the test down.
	b := &ACP{shutdownGrace: 20 * time.Millisecond}
	b.BinaryPath = "/bin/sh"

	tr, err := b.spawnTransport(context.Background(), transportRequest{argv: []string{script, pidFile}, workDir: dir})
	require.NoError(t, err)

	workerPID, err := strconv.Atoi(waitForFile(t, pidFile, 5*time.Second))
	require.NoError(t, err)
	require.Eventually(t, func() bool { return processAlive(workerPID) }, time.Second, 10*time.Millisecond,
		"the backgrounded worker must actually be running before we can prove close() reaps it")

	// close()'s returned error is cmd.Wait()'s — a SIGKILLed process reports
	// "signal: killed" there, expected and not itself under test; what this
	// test proves is the SIDE EFFECT below (the detached worker actually dies).
	_ = tr.close()

	require.Eventually(t, func() bool { return !processAlive(workerPID) }, 2*time.Second, 10*time.Millisecond,
		"close() must reap the WHOLE process group, including a worker the immediate child detached without setsid (moral-scorn) — a plain Process.Kill on just the child leaves this process running")
}

// flushingAgentScript emulates claude-code-acp's ASYNC transcript flush:
// it doesn't exit the instant stdin closes — `cat >/dev/null`
// blocks reading stdin until EOF, then it sleeps briefly (standing in for
// "writing its own native session transcript to disk") before writing a
// marker file and exiting cleanly. A teardown that force-kills the process
// group the instant stdin closes, with no grace period, races this and the
// marker is never written — exactly the shape found against the
// real claude-code-acp binary (a 1-line native transcript with no assistant
// records), reproduced here hermetically with a throwaway shell script.
const flushingAgentScript = `#!/bin/sh
cat >/dev/null
sleep 0.2
echo flushed > "$1"
exit 0
`

// TestSpawnTransport_CloseGivesFlushGracePeriod pins the fix: close()
// must give the spawned agent a bounded grace period to exit ON ITS OWN
// after stdin EOF — long enough for a real async flush to land — before
// force-killing the process group. Before the fix, close() killed
// immediately after stdin.Close(), racing (and here, reliably losing to) the
// script's 0.2s post-EOF work.
func TestSpawnTransport_CloseGivesFlushGracePeriod(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "flushing-agent.sh")
	require.NoError(t, os.WriteFile(script, []byte(flushingAgentScript), 0o755))
	markerFile := filepath.Join(dir, "flushed.marker")

	b := &ACP{shutdownGrace: time.Second} // comfortably longer than the script's 0.2s "flush"
	b.BinaryPath = "/bin/sh"

	tr, err := b.spawnTransport(context.Background(), transportRequest{argv: []string{script, markerFile}, workDir: dir})
	require.NoError(t, err)

	_ = tr.close()

	data, err := os.ReadFile(markerFile)
	require.NoError(t, err, "the spawned agent's post-stdin-EOF flush must be allowed to complete before teardown kills it")
	assert.Equal(t, "flushed\n", string(data))
}

// TestSpawnTransport_CloseKillsAfterGraceExpires proves the OTHER half of the
// contract: a spawned agent that does NOT exit within the grace period is
// still force-killed (bounded), not waited on forever.
func TestSpawnTransport_CloseKillsAfterGraceExpires(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hanging-agent.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ncat >/dev/null\nsleep 100\n"), 0o755))

	b := &ACP{shutdownGrace: 50 * time.Millisecond}
	b.BinaryPath = "/bin/sh"

	tr, err := b.spawnTransport(context.Background(), transportRequest{argv: []string{script}, workDir: dir})
	require.NoError(t, err)

	done := make(chan struct{})
	go func() {
		_ = tr.close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("close() never returned — the grace-period fallback failed to force-kill a hung agent")
	}
}

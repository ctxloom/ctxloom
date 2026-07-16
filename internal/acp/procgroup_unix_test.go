//go:build !windows

package acp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// leakingAgentScript emulates codex-acp's moral-scorn leak: it backgrounds a
// worker WITHOUT setsid (so the worker stays in the SAME process group as
// this script, exactly like the real leak — it is not a full daemonize into
// a new session), writes the worker's pid to $1 for the test to observe, and
// then blocks so the harness's stdin/stdout pipes stay open like a real
// agent's would.
const leakingAgentScript = `#!/bin/sh
( sleep 100 & echo $! > "$1" )
sleep 100
`

// processAlive reports whether pid is a live process, via the kill(2)
// signal-0 idiom (sends no signal, just checks existence/permission).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
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

// TestSpawnTransport_CloseReapsDetachedWorker is moral-scorn's regression
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

	b := &ACP{}
	b.BinaryPath = "/bin/sh"

	tr, err := b.spawnTransport(context.Background(), []string{script, pidFile}, nil, dir)
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

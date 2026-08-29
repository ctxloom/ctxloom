//go:build unix

package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Process-reaping gate. A container
// launch spawns a `docker run` CHILD of this process. Go's os/exec only
// releases that child's kernel process entry when Wait() is called: a
// Start()ed-and-Kill()ed process that is never Waited stays a ZOMBIE
// (`[docker] <defunct>`) for the parent's whole lifetime. A retry loop then
// converts a container-launch bug into slow PID exhaustion — defunct
// docker children have accumulated by the hundreds within under an hour.
//
// These tests assert against the PROCESS TABLE (/proc), not against an API
// return value: the leak is invisible to every return code in the launch
// path. They use a stubbed runtime binary (/bin/sh) so they run under plain
// `just test` with no container runtime present — which means they prove the
// REAPING CONTRACT of the spawn/kill code, not anything about docker itself.

// procState reads a pid's scheduler state letter and parent pid from
// /proc/<pid>/stat. The comm field is parenthesised and may itself contain
// ')', so the scan starts after the LAST ')'.
func procState(pid int) (state byte, ppid int, ok bool) {
	raw, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	i := strings.LastIndex(string(raw), ")")
	if i < 0 || i+2 >= len(raw) {
		return 0, 0, false
	}
	fields := strings.Fields(string(raw[i+2:]))
	if len(fields) < 2 {
		return 0, 0, false
	}
	pp, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], pp, true
}

// zombieChildren lists this process's own unreaped (state Z) children.
// Scoped to our pid deliberately: the machine routinely carries unrelated
// orphaned processes, and a global count would read as signal.
func zombieChildren() []int {
	self := os.Getpid()
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if state, ppid, ok := procState(pid); ok && ppid == self && state == 'Z' {
			out = append(out, pid)
		}
	}
	return out
}

// reapRuntime is a Runtime whose "container" commands are plain shell
// processes, so the spawn/kill/reap lifecycle is exercised end to end with no
// daemon. runArgs is a function of the spec so the fs-probe test can render a
// command that actually reads the marker it mounted.
type reapRuntime struct {
	fakeRuntime
	bin  string
	run  func(RunSpec) []string
	rm   []string
	name string
}

func (r reapRuntime) Name() string               { return r.name }
func (r reapRuntime) Binary() string             { return r.bin }
func (r reapRuntime) Available() bool            { return true }
func (r reapRuntime) RunArgs(s RunSpec) []string { return r.run(s) }
func (r reapRuntime) RemoveArgs(string) []string { return r.rm }

// mapper is inherited from the embedded fakeRuntime (a non-identity
// prefixMapper, container_test.go) — this type used to shadow it with its
// own hardcoded identityMapper{}, but these tests never assert on a mapped
// path, so the override was pure duplication (Reductive Development).

// newReapRuntime builds a shell-backed runtime whose `run` sleeps (a
// long-lived "container" that must be killed) and whose `rm` exits at once.
func newReapRuntime() reapRuntime {
	return reapRuntime{
		name: "docker",
		bin:  "/bin/sh",
		run:  func(RunSpec) []string { return []string{"-c", "sleep 30"} },
		rm:   []string{"-c", "exit 0"},
	}
}

// zombieBaseline snapshots the unreaped children already present, so each test
// asserts on its OWN delta. Tests share one process, and a sibling test that
// leaks (or an unrelated in-flight reap) would otherwise read as this test's
// signal — the measurement-hygiene rule applied inside the suite.
func zombieBaseline() map[int]bool {
	base := map[int]bool{}
	for _, pid := range zombieChildren() {
		base[pid] = true
	}
	return base
}

// requireNoNewZombieChildren polls until this process has gained no unreaped
// children since base, failing with the surviving pids (and their state).
func requireNoNewZombieChildren(t *testing.T, base map[int]bool, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var leaked []int
		for _, pid := range zombieChildren() {
			if !base[pid] {
				leaked = append(leaked, pid)
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			var detail []string
			for _, pid := range leaked {
				state, ppid, _ := procState(pid)
				detail = append(detail, fmt.Sprintf("pid=%d state=%c ppid=%d", pid, state, ppid))
			}
			t.Fatalf("%d unreaped (defunct) child process(es) survive: %s\n"+
				"each container launch attempt leaks a PID; a retry loop turns this into PID exhaustion",
				len(leaked), strings.Join(detail, ", "))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestStartDirectRunner_ReapsRunProcessAfterKill is red-test (a): after N
// launch-and-teardown cycles the child process count must return to baseline.
// startDirectRunner Start()s the `docker run` process and RunnerHandle.Kill
// force-removes the container + signals the process — but nothing in the
// production call graph ever calls RunnerHandle.Wait, so before the fix every
// cycle leaves one defunct child behind.
func TestStartDirectRunner_ReapsRunProcessAfterKill(t *testing.T) {
	base := zombieBaseline()

	const attempts = 5
	rt := newReapRuntime()
	for i := range attempts {
		h, err := startDirectRunner(rt, RunSpec{Name: fmt.Sprintf("ctxloom-reap-probe-%d", i)}, nil)
		require.NoError(t, err)
		h.Kill()
	}

	requireNoNewZombieChildren(t, base, 5*time.Second)
}

// TestProbeOneRoot_ReapsProbeProcess is the fs-probe half of the same
// question. probeExec runs the probe container via exec.CommandContext.Output(),
// which does call Wait internally — so this test MEASURES whether the probe
// path leaks rather than assuming it does. `ctxloom-fsprobe-*` was named as
// a leak site; this pins the answer either way.
func TestProbeOneRoot_ReapsProbeProcess(t *testing.T) {
	base := zombieBaseline()

	rt := newReapRuntime()
	// Render a command that reads the marker back out of the "mounted" dir,
	// so the probe takes its SUCCESS path (the ordinary case).
	rt.run = func(s RunSpec) []string {
		return []string{"-c", "cat " + filepath.Join(s.Mounts[0].Host, "marker")}
	}
	root := t.TempDir()
	for range 5 {
		require.NoError(t, probeOneRoot(context.Background(), rt, "img", root))
	}

	requireNoNewZombieChildren(t, base, 5*time.Second)
}

// TestProbeOneRoot_ReapsCancelledProbeProcess is the failure half: the
// incident's probes were against a runtime that was not answering, so the
// path that matters is the CANCELLED / timed-out one. exec.CommandContext
// kills the process on cancel, but Wait (and therefore the reap) only
// completes once the output-copy goroutines finish — a probe process that
// leaves a descendant holding the stdout pipe would both hang the caller and
// leave the child unreaped. This asserts the cancelled probe returns promptly
// AND is reaped.
func TestProbeOneRoot_ReapsCancelledProbeProcess(t *testing.T) {
	base := zombieBaseline()

	rt := newReapRuntime()
	// A probe "container" that outlives its own shell: the backgrounded sleep
	// inherits the stdout pipe, the shape that turns a kill into a hang.
	rt.run = func(RunSpec) []string { return []string{"-c", "sleep 30 & wait"} }
	root := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- probeOneRoot(ctx, rt, "img", root) }()
	select {
	case err := <-done:
		require.Error(t, err, "a cancelled probe must report failure, not a phantom sharing verdict")
	case <-time.After(10 * time.Second):
		t.Fatal("probeOneRoot did not return after its context was cancelled: the probe exec hangs on a descendant holding the output pipe, so the killed child is never reaped")
	}

	requireNoNewZombieChildren(t, base, 5*time.Second)
}

package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AwaitContainerRunning exists because StartRunner returning is NOT the
// container running: the exec that follows was measured being issued before the
// `run` reached the daemon, failing with a "No such container" that named
// nothing while the real reason sat unread in the runner's stderr.
//
// The arm pinned here is the one that carries that reason. A runner that exits
// before the container comes up must fail FAST (on the exit signal, never on
// the 30s backstop) and must carry the runner's stderr into the error, because
// --rm destroys the container and `logs` is then too late.
func TestAwaitContainerRunning_ExitingRunnerFailsWithItsStderr(t *testing.T) {
	// Binary is /bin/sh, so `container inspect` never reports running —
	// the container-is-up arm can never fire and the exit arm must decide.
	rt := newReapRuntime()

	h := &RunnerHandle{
		Name:       "ctxloom-iso-probe-dead",
		Wait:       func() error { return errors.New("exit status 7") },
		StderrTail: func() string { return "RUNNER-BOOM-DIAGNOSTIC" },
	}

	err := AwaitContainerRunning(rt, h)
	require.Error(t, err, "a runner that exited before its container started must not report ready")
	require.Contains(t, err.Error(), "RUNNER-BOOM-DIAGNOSTIC",
		"the runner's stderr is the only copy of the reason; an error without it is the silence this fixes")
	require.Contains(t, err.Error(), "ctxloom-iso-probe-dead", "the error must name the container")
	require.NotContains(t, strings.ToLower(err.Error()), "was not running after",
		"must fail on the EXIT signal, not by timing out on the backstop")
}

// A runtime that cannot be inspected at all (Host, or any fake) must not stall
// a caller for the full backstop: there is no daemon to ask, so there is
// nothing to wait for.
func TestAwaitContainerRunning_NoRuntimeBinaryIsImmediatelyReady(t *testing.T) {
	require.NoError(t, AwaitContainerRunning(Host{}, &RunnerHandle{Name: "irrelevant"}),
		"a runtime with no binary cannot be inspected and must not block")
	require.NoError(t, AwaitContainerRunning(newReapRuntime(), nil),
		"a nil handle has no container to wait for")
}

// The HAPPY path: once the container is observed running, the barrier returns
// ready and the caller may exec. Without this, only the failure arms were
// pinned — and a barrier that never reports ready would have passed them all
// while blocking every container run for the full backstop.
//
// No daemon: containerObservedRunning shells out to the runtime binary and
// compares stdout to "true", so a stub binary that prints it is a faithful
// stand-in for an inspect that says the container is up.
func TestAwaitContainerRunning_ObservedRunningIsReady(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-runtime")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho true\n"), 0o755))

	rt := reapRuntime{name: "docker", bin: bin, run: func(RunSpec) []string { return nil }}

	// The runner is alive: its waiter never fires, so ONLY the observed-running
	// arm can end this call. Released at test end so the goroutine does not leak.
	alive := make(chan struct{})
	t.Cleanup(func() { close(alive) })

	h := &RunnerHandle{
		Name:       "ctxloom-iso-alive-probe",
		Wait:       func() error { <-alive; return nil },
		StderrTail: func() string { return "" },
	}

	done := make(chan error, 1)
	go func() { done <- AwaitContainerRunning(rt, h) }()

	select {
	case err := <-done:
		require.NoError(t, err, "a container the runtime reports running must be ready")
	case <-time.After(5 * time.Second):
		t.Fatal("barrier did not report ready for a running container — it would stall every container run")
	}
}

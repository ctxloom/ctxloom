package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachRuntime is a fakeRuntime whose RunArgs renders a real (if minimal) argv,
// so RunAttached execs the stub script in Binary. RemoveArgs stays nil and the
// specs below carry no Name, so teardown never shells out to a remove.
type attachRuntime struct{ fakeRuntime }

func (attachRuntime) RunArgs(RunSpec) []string { return []string{"run"} }

// attachScript writes a stub "container runtime" that behaves like an engine
// adapter ending a conversation: it reads stdin to EOF, then spends `flushFor`
// finishing its own asynchronous work before exiting cleanly.
func attachScript(t *testing.T, flushFor string, marker string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-attached-runtime")
	body := fmt.Sprintf("#!/bin/sh\ncat >/dev/null\nsleep %s\nprintf flushed > %q\n", flushFor, marker)
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

// TestRunAttached_CloseWaitsForTheContainerToFinishFlushing is the container
// half of the teardown-grace contract the host transport already honors: an
// engine that flushes its native transcript after the conversation ends must be
// allowed to finish before the container is destroyed. Removing the container
// takes the filesystem the half-written transcript lives on with it, so this
// loss is unrecoverable rather than merely truncated.
func TestRunAttached_CloseWaitsForTheContainerToFinishFlushing(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "flushed")
	script := attachScript(t, "0.2", marker)

	rt := attachRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{})
	require.NoError(t, err)
	ac.ShutdownGrace = 5 * time.Second // comfortably longer than the stub's flush

	require.NoError(t, ac.Close())

	data, err := os.ReadFile(marker)
	require.NoError(t, err, "the container's post-stdin-EOF flush must complete before teardown destroys it")
	assert.Equal(t, "flushed", string(data))
}

// TestRunAttached_CloseIsBoundedWhenTheContainerIgnoresEOF proves the other half:
// the grace is a bound, not a wait. A container that never reacts to stdin
// closing is still force-removed.
func TestRunAttached_CloseIsBoundedWhenTheContainerIgnoresEOF(t *testing.T) {
	script := attachScript(t, "100", filepath.Join(t.TempDir(), "never"))

	rt := attachRuntime{fakeRuntime{name: "docker", binary: script, available: true}}
	ac, err := RunAttached(context.Background(), rt, RunSpec{})
	require.NoError(t, err)
	ac.ShutdownGrace = 50 * time.Millisecond

	done := make(chan struct{})
	go func() {
		_ = ac.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close must be bounded by the grace period, not wait on a container that ignores stdin EOF")
	}
}

// TestRunAttached_RuntimeThatLaunchesNoContainer is U062-F04's regression.
// interactiveRunArgs indexed args[0] under a comment claiming "Args always start
// with 'run' (every Runtime.RunArgs implementation here does)". That invariant is
// FALSE: Host.RunArgs returns nil — Host launches no container at all — and so
// does any Runtime stub. RunAttached is exported and takes a Runtime, so an empty
// argv reached the index and panicked with index-out-of-range instead of telling
// the caller its runtime cannot start a container.
func TestRunAttached_RuntimeThatLaunchesNoContainer(t *testing.T) {
	assert.Empty(t, interactiveRunArgs(nil), "an empty argv has no insertion point; never index into it")

	require.NotPanics(t, func() {
		_, err := RunAttached(context.Background(), Host{}, RunSpec{})
		require.Error(t, err, "a runtime that renders no run argv must be refused, not indexed into")
		assert.Contains(t, err.Error(), "cannot start a container")
	})
}

// TestInteractiveRunArgs_InsertsAfterRun pins the positive shape the fix must not
// disturb: `-i` lands immediately after the leading "run" verb, keeping stdin
// open for a caller speaking its own stdio protocol, with every other argument in
// its original order.
func TestInteractiveRunArgs_InsertsAfterRun(t *testing.T) {
	assert.Equal(t,
		[]string{"run", "-i", "--rm", "--name", "c", "img"},
		interactiveRunArgs([]string{"run", "--rm", "--name", "c", "img"}))
	assert.Equal(t, []string{"run", "-i"}, interactiveRunArgs([]string{"run"}))
}

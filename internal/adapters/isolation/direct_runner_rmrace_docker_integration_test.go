//go:build docker_integration

package isolation

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// rmRaceRuntime is the real docker runtime seen through
// dockergate.RemoveBeforeCreateWrapper, which forces a teardown's first remove
// to reach the daemon before the launch's create and to return only after it.
type rmRaceRuntime struct {
	Runtime
	bin string
}

func (r rmRaceRuntime) Binary() string { return r.bin }

// rmRaceRuntimes are the runtimes the forced-order tests run under, each gated
// on its own presence: a race proven closed on one daemon says nothing about
// another's rm/create semantics.
var rmRaceRuntimes = []struct {
	name      string
	available func() bool
}{
	{"docker", Docker{}.Available},
	{"podman", Podman{}.Available},
}

// rmRaceImage is fully qualified so no runtime's short-name policy decides it.
const rmRaceImage = "docker.io/library/alpine:latest"

// forEachRmRaceRuntime runs body once per runtime, as a subtest named for it,
// against that runtime seen through the forcing wrapper, with a cleanup that
// removes the container name whatever the outcome.
func forEachRmRaceRuntime(t *testing.T, what, prefix string, body func(t *testing.T, rt Runtime, name string)) {
	for _, r := range rmRaceRuntimes {
		t.Run(r.name, func(t *testing.T) {
			dockergate.RequireNamedRuntime(t, r.name, r.available(), what)
			real := ProbeRuntime(r.name)
			require.Equal(t, r.name, real.Name(), "the probe substituted another runtime")
			name := containerName(prefix)
			t.Cleanup(func() { _ = exec.Command(real.Binary(), "rm", "-f", name).Run() })
			body(t, rmRaceRuntime{Runtime: real, bin: dockergate.RemoveBeforeCreateWrapper(t, real.Binary(), name)}, name)
		})
	}
}

// TestStartDirectRunner_KillBeforeCreateLeavesNoContainer: a Kill whose remove
// reaches the daemon BEFORE the launch's create does must still leave no
// container behind. The remove answers "No such container" (benign-looking),
// the create then lands, and if Kill trusts that answer and SIGKILLs the
// attached CLI, the container is orphaned: --rm never fires for a container
// that was never started, and one that was started outlives its CLI.
func TestStartDirectRunner_KillBeforeCreateLeavesNoContainer(t *testing.T) {
	forEachRmRaceRuntime(t, "the kill-before-create race test", "rmrace", func(t *testing.T, rt Runtime, name string) {
		h, err := startDirectRunner(rt, RunSpec{Image: rmRaceImage, Name: name, Command: []string{"sleep", "300"}}, nil)
		require.NoError(t, err)

		h.Kill()
		_ = h.Wait()

		require.Empty(t, dockergate.ContainersNamed(t, rt.Binary(), name),
			"Kill must not orphan a container whose create landed after its remove said \"No such container\"")
	})
}

// TestRunAttached_CloseBeforeCreateLeavesNoContainer: the attached-stdio
// teardown has the same exposure as RunnerHandle.Kill — RunAttached returns
// once the `run` CLI is spawned, so a Close can reach its remove before the
// daemon has created anything. The remove answers "No such container"; if
// Close trusts it and kills the CLI, the create lands behind it and the
// container is orphaned.
func TestRunAttached_CloseBeforeCreateLeavesNoContainer(t *testing.T) {
	forEachRmRaceRuntime(t, "the close-before-create race test", "rmrace-attached", func(t *testing.T, rt Runtime, name string) {
		ac, err := RunAttached(context.Background(), rt, RunSpec{Image: rmRaceImage, Name: name, Command: []string{"sleep", "300"}}, nil)
		require.NoError(t, err)
		// The grace only waits for an exit the held launch cannot produce; keep
		// it short so the remove is what reaches the daemon first.
		ac.ShutdownGrace = time.Millisecond
		_ = ac.Close()

		require.Empty(t, dockergate.ContainersNamed(t, rt.Binary(), name),
			"Close must not orphan a container whose create landed after its remove said \"No such container\"")
	})
}

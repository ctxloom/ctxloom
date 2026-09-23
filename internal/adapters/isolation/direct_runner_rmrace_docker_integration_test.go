//go:build docker_integration

package isolation

import (
	"context"
	"os/exec"
	"strings"
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

// containersNamed lists every container, in any state, whose name is exactly
// name — the leak this test is about is often a CREATED container that never
// started, which `docker ps` without -a does not show.
func containersNamed(t *testing.T, name string) []string {
	t.Helper()
	out, err := exec.Command("docker", "ps", "-a", "--filter", "name=^/"+name+"$",
		"--format", "{{.Names}} {{.Status}}").Output()
	require.NoError(t, err)
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestStartDirectRunner_KillBeforeCreateLeavesNoContainer: a Kill whose remove
// reaches the daemon BEFORE the launch's create does must still leave no
// container behind. The remove answers "No such container" (benign-looking),
// the create then lands, and if Kill trusts that answer and SIGKILLs the
// attached CLI, the container is orphaned: --rm never fires for a container
// that was never started, and one that was started outlives its CLI.
func TestStartDirectRunner_KillBeforeCreateLeavesNoContainer(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the kill-before-create race test")

	name := containerName("rmrace")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	rt := rmRaceRuntime{Runtime: ProbeRuntime("docker"), bin: dockergate.RemoveBeforeCreateWrapper(t, "docker", name)}
	spec := RunSpec{Image: "alpine:latest", Name: name, Command: []string{"sleep", "300"}}

	h, err := startDirectRunner(rt, spec, nil)
	require.NoError(t, err)

	h.Kill()
	_ = h.Wait()

	require.Empty(t, containersNamed(t, name),
		"Kill must not orphan a container whose create landed after its remove said \"No such container\"")
}

// TestRunAttached_CloseBeforeCreateLeavesNoContainer: the attached-stdio
// teardown has the same exposure as RunnerHandle.Kill — RunAttached returns
// once the `run` CLI is spawned, so a Close can reach its remove before the
// daemon has created anything. The remove answers "No such container"; if
// Close trusts it and kills the CLI, the create lands behind it and the
// container is orphaned.
func TestRunAttached_CloseBeforeCreateLeavesNoContainer(t *testing.T) {
	dockergate.RequireRuntime(t, (Docker{}).Available(), "the close-before-create race test")

	name := containerName("rmrace-attached")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })

	rt := rmRaceRuntime{Runtime: ProbeRuntime("docker"), bin: dockergate.RemoveBeforeCreateWrapper(t, "docker", name)}
	spec := RunSpec{Image: "alpine:latest", Name: name, Command: []string{"sleep", "300"}}

	ac, err := RunAttached(context.Background(), rt, spec, nil)
	require.NoError(t, err)
	// The grace only waits for an exit the held launch cannot produce; keep it
	// short so the remove is what reaches the daemon first.
	ac.ShutdownGrace = time.Millisecond
	_ = ac.Close()

	require.Empty(t, containersNamed(t, name),
		"Close must not orphan a container whose create landed after its remove said \"No such container\"")
}

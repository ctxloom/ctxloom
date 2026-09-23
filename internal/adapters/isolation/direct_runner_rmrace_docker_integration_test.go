//go:build docker_integration

package isolation

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/dockergate"
)

// rmRaceRuntime is the real docker runtime seen through a wrapper binary that
// FORCES one interleaving of RunnerHandle.Kill against an in-flight launch:
//
//   - `run` is held until teardown has begun (the first `rm` or `inspect`),
//     so the daemon has not been asked to create anything when Kill's first
//     remove reaches it;
//   - that first `rm` really runs — and so deterministically answers "No such
//     container" — then does not return until the daemon HAS registered the
//     container, so the CLI kill that follows it lands after the create.
//
// That is the race the handle is exposed to in production: RunnerStarter hands
// h.Kill to the run's holder the moment the CLI is spawned, before
// AwaitContainerRunning has seen anything, so a teardown can arrive while the
// create is still in flight.
type rmRaceRuntime struct {
	Runtime
	bin string
}

func (r rmRaceRuntime) Binary() string { return r.bin }

func writeRmRaceWrapper(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	began := filepath.Join(dir, "teardown-began")
	firstRm := filepath.Join(dir, "first-rm")
	script := fmt.Sprintf(`#!/bin/sh
case "$1" in
run)
	i=0
	while [ ! -e %[1]q ]; do
		i=$((i+1)); [ $i -gt 2000 ] && { echo "wrapper: teardown never began" >&2; exit 97; }
		sleep 0.01
	done
	exec docker "$@" ;;
rm)
	touch %[1]q
	if [ -e %[2]q ]; then exec docker "$@"; fi
	touch %[2]q
	err=$(docker "$@" 2>&1 >/dev/null); rc=$?
	i=0
	until docker container inspect %[3]q >/dev/null 2>&1; do
		i=$((i+1)); [ $i -gt 2000 ] && break
		sleep 0.01
	done
	printf '%%s\n' "$err" >&2
	exit $rc ;;
container)
	touch %[1]q
	exec docker "$@" ;;
esac
exec docker "$@"
`, began, firstRm, name)
	bin := filepath.Join(dir, "docker-rmrace")
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin
}

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

	rt := rmRaceRuntime{Runtime: ProbeRuntime("docker"), bin: writeRmRaceWrapper(t, name)}
	spec := RunSpec{Image: "alpine:latest", Name: name, Command: []string{"sleep", "300"}}

	h, err := startDirectRunner(rt, spec, nil)
	require.NoError(t, err)

	h.Kill()
	_ = h.Wait()

	require.Empty(t, containersNamed(t, name),
		"Kill must not orphan a container whose create landed after its remove said \"No such container\"")
}

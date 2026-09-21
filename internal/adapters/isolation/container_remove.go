package isolation

import (
	"errors"
	"os/exec"
	"strings"
	"time"
)

// containerRemoveTimeout bounds our OWN teardown: a `docker/podman rm -f`
// against a wedged daemon would otherwise hang session shutdown forever. We
// cap it here regardless of the ctx passed in.
const containerRemoveTimeout = 15 * time.Second

// removeReportsGone reports whether a force-remove error is the benign
// already-gone race (the --rm `run` beat us to removing the container):
// docker/podman exit non-zero with "No such container" — the container had
// ALREADY finished self-removing before our `rm -f` ran — or "removal of
// container ... is already in progress" — docker's OWN async --rm cleanup
// is in flight AT THE SAME MOMENT ours lands (a long-lived container that
// only exits when its stdin closes reliably reproduces this message; a
// short-lived one never does, because by the time teardown runs its --rm
// has always already finished). Both are teardown SUCCESS — the container is
// gone (or is guaranteed to be, momentarily, by docker's own in-flight
// removal) — not a leak, so neither is surfaced.
func removeReportsGone(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	stderr := strings.ToLower(string(ee.Stderr))
	return strings.Contains(stderr, "no such container") ||
		(strings.Contains(stderr, "removal of container") && strings.Contains(stderr, "already in progress"))
}

// containerBaseEnv is the fixed env every runner container gets, independent
// of auth. IS_SANDBOX=1 tells the engine it runs inside a real sandbox so it
// permits approval-bypass as root — the container runs as (mapped) root, and
// claude otherwise REFUSES `--dangerously-skip-permissions` under uid 0
// ("cannot be used with root/sudo privileges"). This is the runtime-side half
// of "the container is the boundary": the run's actual approval posture
// resolves from config/CLI/agent, never from the isolation policy. Harmless
// to engines that ignore it.
var containerBaseEnv = []string{"IS_SANDBOX=1"}

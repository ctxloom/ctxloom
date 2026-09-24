package isolation

import (
	"time"
)

// containerRemoveTimeout bounds our OWN teardown: a runtime `rm -f`
// against a wedged daemon would otherwise hang session shutdown forever. We
// cap it here regardless of the ctx passed in.
const containerRemoveTimeout = 15 * time.Second

// containerBaseEnv is the fixed env every runner container gets, independent
// of auth. IS_SANDBOX=1 tells the engine it runs inside a real sandbox so it
// permits approval-bypass as root — the container runs as (mapped) root, and
// claude otherwise REFUSES `--dangerously-skip-permissions` under uid 0
// ("cannot be used with root/sudo privileges"). This is the runtime-side half
// of "the container is the boundary": the run's actual approval posture
// resolves from config/CLI/agent, never from the isolation policy. Harmless
// to engines that ignore it.
var containerBaseEnv = []string{"IS_SANDBOX=1"}

package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// RunnerStarter is the owner run's starter for a launch whose runner is a
// plain process: the cell's transport (Policy.StartRunner — a bare
// self-invoked `ctxloom runner` on the host, `docker run … ctxloom runner`
// for a container). A container is awaited HERE, inside the starter: the
// very next thing StartOwnedRun does is wait for the runner to dial home,
// which a container that never came up can never do. started receives the
// handle the moment the process exists, so a container that failed to reach
// running is still torn down by its holder.
func RunnerStarter(cell PreparedCell, backend, label string, verbosity int, started func(*isolation.RunnerHandle)) coord.OwnedRunStarter {
	return func(ctx context.Context, spawnEnv map[string]string) (func(), string, error) {
		h, err := cell.Policy.StartRunner(ctx, backend, label, verbosity, cell.Workspace, spawnEnv)
		if err != nil {
			return nil, "", err
		}
		if started != nil {
			started(h)
		}
		if h.Name == "" {
			return h.Kill, "", nil
		}
		if rerr := isolation.AwaitContainerRunning(RuntimeForPolicy(cell.Policy), h); rerr != nil {
			// NON-DEGRADABLE: a boundary that was requested, accepted, and
			// then died must not launch on the host in either mode.
			strictness.FailAlways(strictness.ClassIsolation,
				"check the container runtime and the agent image can start (`docker logs `/`podman logs ` the named container); this run cannot fall back to the host without silently dropping the boundary it was given",
				"container %q was started but never reached running state, so the isolation it promised does not exist: %v", h.Name, rerr)
			return h.Kill, h.Name, rerr
		}
		return h.Kill, h.Name, nil
	}
}

package operations

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// RunnerStarter is the owner run's starter for a launch whose runner is a
// plain process: the environment's own Start (a bare self-invoked `ctxloom
// runner` on the host, `docker run … ctxloom runner` for a container, which
// the environment awaits to running before it returns). started receives the
// handle the moment the runner is up, so its holder can tear it down.
func RunnerStarter(env isolation.Environment, backend, label string, verbosity int, started func(*isolation.RunnerHandle)) coord.OwnedRunStarter {
	return func(ctx context.Context, spawnEnv map[string]string) (coord.OwnedRunner, error) {
		h, err := env.Start(ctx, isolation.RunnerRequest{Engine: backend, Label: label, Verbosity: verbosity, Env: spawnEnv})
		if err != nil {
			return coord.OwnedRunner{}, err
		}
		if started != nil {
			started(h)
		}
		return coord.OwnedRunner{Kill: h.Kill, Wait: isolation.WaitOf(h), ContainerName: h.Name}, nil
	}
}

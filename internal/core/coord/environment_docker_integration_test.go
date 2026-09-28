//go:build docker_integration

package coord_test

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// unreachableDockerHost points the docker CLI at a socket nothing listens on,
// so a run-path selection that probes docker first moves on to podman.
const unreachableDockerHost = "unix:///nonexistent/ctxloom-test-docker.sock"

// containerAxes is the container axis runtimeName can serve: the ownership
// the named runtime has on this host.
func containerAxes(runtimeName string) launch.Axes {
	axis := launch.RuntimeRootless
	if isolation.SelectRuntime(runtimeName, axis).Name() != runtimeName {
		axis = launch.RuntimeRootful
	}
	return launch.Axes{Workspace: launch.WorkspaceNone, Runtime: axis}
}

// preparedContainer prepares the REAL container Environment a run of backend
// gets on runtimeName, through isolation.Prepare — the production path — over
// image run as-is, in state's session. A prepare that degraded off the
// container, or landed on another runtime, is an error: the test would
// otherwise measure a host run.
//
// Selection probes docker before podman; a podman caller must have made
// docker unreachable for its own duration first (withPodmanSelected).
func preparedContainer(ctx context.Context, runtimeName, backend, image, projectDir string, state isolation.SessionState) (isolation.Environment, error) {
	eng, ok := engines.Registry().Lookup(engine.Name(backend))
	if !ok {
		return nil, fmt.Errorf("engine %q is not registered", backend)
	}
	sessionDir, err := paths.HarpDir(state.Harp)
	if err != nil {
		return nil, err
	}
	spec, err := isolation.NewSpec(containerAxes(runtimeName), eng).
		Project(projectDir).
		Session(state.Harp, sessionDir, state).
		Image(isolation.ImageConfig{Image: image}).
		Build()
	if err != nil {
		return nil, err
	}
	env, err := isolation.Prepare(ctx, spec)
	if err != nil {
		return nil, err
	}
	if got := env.Describe().Runtime; got != runtimeName {
		_ = env.Cleanup()
		return nil, fmt.Errorf("prepared on %q, not %q: the container was not provided", got, runtimeName)
	}
	return env, nil
}

// withPodmanSelected makes docker unreachable for the caller's duration when
// runtimeName is podman, so the run-path selection lands on podman.
func withPodmanSelected(t interface{ Setenv(k, v string) }, runtimeName string) {
	if runtimeName == "podman" {
		t.Setenv("DOCKER_HOST", unreachableDockerHost)
	}
}

// removeOnRunExit is the attach remover an originator builds from an
// interactive runner's Teardown: the teardown, with a ctx that is done once
// the run CLI has exited.
func removeOnRunExit(teardown func(context.Context) error) func(runExited <-chan struct{}) {
	return func(runExited <-chan struct{}) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			select {
			case <-runExited:
				cancel()
			case <-ctx.Done():
			}
		}()
		_ = teardown(ctx)
	}
}

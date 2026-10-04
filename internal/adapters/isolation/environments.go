package isolation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// hostEnvironment is the host Environment: workspace none or worktree, the
// runner a bare self-invoked process in it.
type hostEnvironment struct {
	p         policy
	ws        workspace
	placement launch.Placement
	axis      WorkspaceAxis
	// state is the run's session, whose scratch dir holds the secrets file
	// when the platform has no per-user tmpfs.
	state SessionState
	// secrets is the run's secrets file, made by the first runner start that
	// carries a credential; released by Cleanup.
	secrets *secretsFile
}

func (e *hostEnvironment) Placement() launch.Placement { return e.placement }

// Listen is zero: a host runner dials the coordinator's loopback listener.
func (*hostEnvironment) Listen() present.Listen { return present.Listen{} }

func (e *hostEnvironment) Start(ctx context.Context, r RunnerRequest) (*RunnerHandle, error) {
	env, err := e.stageCred(r.Env)
	if err != nil {
		return nil, err
	}
	return e.p.startRunner(ctx, r.Engine, r.Label, r.Verbosity, e.ws, env)
}

// Interactive is the self-exec'd runner; nothing beyond the process is
// started, so there is no teardown (the secrets file goes with Cleanup).
func (e *hostEnvironment) Interactive(ctx context.Context, r RunnerRequest) (Interactive, error) {
	env, err := e.stageCred(r.Env)
	if err != nil {
		return Interactive{}, err
	}
	cmd, _, err := e.p.interactiveRunner(ctx, r.Engine, e.ws, env)
	if err != nil {
		return Interactive{}, err
	}
	return Interactive{Cmd: cmd}, nil
}

// stageCred moves the coordinator credential in spawnEnv into the run's
// secrets file (stageCoordCred), making the file on first need. A host runner
// opens it at its host path. Where the platform has no per-user tmpfs the
// secret dir goes on disk under the session's scratch dir; a run with no
// usable harp has none, and is refused rather than given a shared dir.
func (e *hostEnvironment) stageCred(spawnEnv map[string]string) (map[string]string, error) {
	if _, ok := spawnEnv[sessions.EnvCoordCred]; !ok {
		return spawnEnv, nil
	}
	if e.secrets == nil {
		diskParent, err := e.state.scratchDir()
		if err != nil {
			return nil, fmt.Errorf("run secrets: %w", err)
		}
		f, err := newSecretsFile(diskParent)
		if err != nil {
			return nil, err
		}
		e.secrets = f
	}
	return stageCoordCred(e.secrets, spawnEnv, e.secrets.path())
}

func (e *hostEnvironment) Describe() Description { return hostDescription(e.axis) }

// SecretsFile is "": a host run's secrets file is made only as its runner
// starts, and a host runner ends with the process that started it, so no
// restart ever re-adopts one to refresh.
func (*hostEnvironment) SecretsFile() string { return "" }

// Cleanup removes the run's secrets file, then the workspace.
func (e *hostEnvironment) Cleanup() error {
	var errs error
	if e.secrets != nil {
		f := e.secrets
		e.secrets = nil
		errs = f.release()
	}
	return errors.Join(errs, e.ws.Cleanup())
}

func hostDescription(axis WorkspaceAxis) Description {
	return Description{Workspace: string(axis), Runtime: Host{}.Name(), Reach: "loopback"}
}

// containerEnvironment is the ONE container Environment, over any Runtime
// (docker, podman; rootless or rootful): per-runtime differences stay behind
// the Runtime seam.
type containerEnvironment struct {
	c         Container
	cw        *containerWorkspace
	placement launch.Placement
}

func (e *containerEnvironment) Placement() launch.Placement { return e.placement }

func (e *containerEnvironment) Listen() present.Listen { return e.cw.reach.listen }

// Start launches the runner as the container's foreground process and
// returns once the container is OBSERVED running: a container that never
// came up could never dial home, and waiting for that dial would only hide
// the reason. A container that dies first is torn down here and refused —
// NON-DEGRADABLE, because a boundary that was requested, accepted, and then
// died must not launch on the host in either mode.
func (e *containerEnvironment) Start(ctx context.Context, r RunnerRequest) (*RunnerHandle, error) {
	h, err := e.c.startRunner(ctx, r.Engine, r.Label, r.Verbosity, e.cw, r.Env)
	if err != nil {
		return nil, err
	}
	if err := AwaitContainerRunning(e.c.runtime, h); err != nil {
		strictness.FailAlways(report.KindIsolation,
			"check the container runtime and the agent image can start (`docker logs `/`podman logs ` the named container); this run cannot fall back to the host without silently dropping the boundary it was given",
			"container %q was started but never reached running state, so the isolation it promised does not exist: %v", h.Name, err)
		h.Kill()
		return nil, err
	}
	return h, nil
}

// Interactive is `run -i -t … ctxloom runner <engine>`, named so teardown
// can remove the container the daemon keeps running past its CLI.
func (e *containerEnvironment) Interactive(ctx context.Context, r RunnerRequest) (Interactive, error) {
	cmd, name, err := e.c.interactiveRunner(ctx, r.Engine, e.cw, r.Env)
	if err != nil {
		return Interactive{}, err
	}
	return Interactive{Cmd: cmd, Name: name, Teardown: func(ctx context.Context) error {
		e.c.remove(ctx, name)
		return nil
	}}, nil
}

func (e *containerEnvironment) Describe() Description { return e.c.describe(e.cw.reach) }

// SecretsFile is the host side of the file mounted at secretsTarget, "" for
// a run with no secret.
func (e *containerEnvironment) SecretsFile() string {
	if e.cw.secrets == nil {
		return ""
	}
	return e.cw.secrets.path()
}

func (e *containerEnvironment) Cleanup() error { return e.cw.Cleanup() }

// --- the policy hooks ------------------------------------------------------

func (None) relocator() relocator { return hostRelocator{} }

func (n None) environment(ws workspace, pl launch.Placement, _ []mount, _ engine.Credentials) (Environment, error) {
	return &hostEnvironment{p: n, ws: ws, placement: pl, axis: WorkspaceShared, state: n.state}, nil
}

func (None) preview(context.Context) (present.Listen, Description) {
	return present.Listen{}, hostDescription(WorkspaceShared)
}

func (Worktree) relocator() relocator { return hostRelocator{} }

func (w Worktree) environment(ws workspace, pl launch.Placement, _ []mount, _ engine.Credentials) (Environment, error) {
	return &hostEnvironment{p: w, ws: ws, placement: pl, axis: WorkspaceWorktree, state: w.state}, nil
}

func (Worktree) preview(context.Context) (present.Listen, Description) {
	return present.Listen{}, hostDescription(WorkspaceWorktree)
}

func (c Container) relocator() relocator {
	return containerRelocator{rt: c.runtime, instanceHome: c.instanceHome, home: c.home}
}

// environment binds the relocator's outcome onto the workspace the runner
// spec renders: the project root's Engine side is the container's workdir,
// and the root mounts are the ones the relocator produced with it.
func (c Container) environment(ws workspace, pl launch.Placement, roots []mount, creds engine.Credentials) (Environment, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return nil, fmt.Errorf("container environment: unexpected workspace %T (expected a container workspace)", ws)
	}
	if len(pl.SecretFiles) > 0 {
		if cw.secrets == nil {
			return nil, errSecretUnstaged
		}
		if err := materializeSecrets(cw.secrets, pl, creds); err != nil {
			return nil, err
		}
	}
	cw.workDir = pl.Paths.Paths().ProjectRoot.Engine
	cw.roots = roots
	return &containerEnvironment{c: c, cw: cw, placement: pl}, nil
}

// preview probes the route home (network inspect) the way resolveWorkspace
// does, recording what a run would refuse; a failed probe leaves the reach
// unknown.
func (c Container) preview(ctx context.Context) (present.Listen, Description) {
	route, err := settleReach(ctx, c.runtime)
	if err != nil {
		desc := c.describe(hostRoute{})
		desc.Reach = ReachUnknown
		return present.Listen{}, desc
	}
	return route.listen, c.describe(route)
}

// describe names this container for display.
func (c Container) describe(route hostRoute) Description {
	axis := WorkspaceShared
	if c.Name() == PolicyNameContainerWorktree {
		axis = WorkspaceWorktree
	}
	reach := "loopback"
	switch {
	case route.listen.Public:
		reach = route.dial + " (public)"
	case strings.HasPrefix(route.network, sharedNamespacePrefix):
		reach = "loopback (sharing this process's container network namespace)"
	case route.dial != "" && route.dial == route.listen.Addr && route.network != "":
		reach = fmt.Sprintf("%s (container network %s)", route.dial, route.network)
	case route.dial != "":
		reach = route.dial
	}
	return Description{Workspace: string(axis), Runtime: runtimeName(c.runtime), Reach: reach}
}

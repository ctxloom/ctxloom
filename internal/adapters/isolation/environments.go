package isolation

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
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
}

func (e *hostEnvironment) Placement() launch.Placement { return e.placement }

// Listen is zero: a host runner dials the coordinator's loopback listener.
func (*hostEnvironment) Listen() present.Listen { return present.Listen{} }

func (e *hostEnvironment) Start(ctx context.Context, r RunnerRequest) (*RunnerHandle, error) {
	return e.p.startRunner(ctx, r.Engine, r.Label, r.Verbosity, e.ws, r.Env)
}

// Interactive is the self-exec'd runner; nothing beyond the process is
// started, so there is no teardown.
func (e *hostEnvironment) Interactive(ctx context.Context, r RunnerRequest) (Interactive, error) {
	cmd, _, err := e.p.interactiveRunner(ctx, r.Engine, e.ws, r.Env)
	if err != nil {
		return Interactive{}, err
	}
	return Interactive{Cmd: cmd}, nil
}

func (e *hostEnvironment) Describe() Description { return hostDescription(e.axis) }

func (e *hostEnvironment) Cleanup() error { return e.ws.Cleanup() }

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

func (e *containerEnvironment) Cleanup() error { return e.cw.Cleanup() }

// --- the policy hooks ------------------------------------------------------

func (None) relocator() relocator { return hostRelocator{} }

func (n None) environment(ws workspace, pl launch.Placement, _ []mount) (Environment, error) {
	return &hostEnvironment{p: n, ws: ws, placement: pl, axis: WorkspaceShared}, nil
}

func (None) preview(context.Context) (present.Listen, Description) {
	return present.Listen{}, hostDescription(WorkspaceShared)
}

func (Worktree) relocator() relocator { return hostRelocator{} }

func (w Worktree) environment(ws workspace, pl launch.Placement, _ []mount) (Environment, error) {
	return &hostEnvironment{p: w, ws: ws, placement: pl, axis: WorkspaceWorktree}, nil
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
func (c Container) environment(ws workspace, pl launch.Placement, roots []mount) (Environment, error) {
	cw, ok := ws.(*containerWorkspace)
	if !ok {
		return nil, fmt.Errorf("container environment: unexpected workspace %T (expected a container workspace)", ws)
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
	case route.dial != "":
		reach = route.dial
	}
	return Description{Workspace: string(axis), Runtime: runtimeName(c.runtime), Reach: reach}
}

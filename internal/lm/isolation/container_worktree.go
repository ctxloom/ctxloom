package isolation

import (
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/git"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// ContainerWorktree is the worktree-in-container policy: the
// {Workspace} × {Runtime} composition (plan §3) whose Workspace is a per-agent
// git worktree (Phase 2) and whose Runtime is a container (Phase 1). The axes
// {workspace: worktree, runtime: container} resolve HERE (via Prepare), NOT to
// the Container policy (which mounts the LIVE project dir): the run's own
// worktree is bind-mounted identical-path into a fresh container as cwd, so
// concurrent runs never share a checkout AND the engine is contained. It REUSES the two proven
// halves — the Worktree policy's WIP-safe, nested-aware lifecycle (create + §3.1
// excludes/skip-worktree + teardown) and the Container policy's scratch/auth/spawn
// — rather than duplicating either. (A full orthogonal Workspace×Runtime refactor
// of all three policies is the cleaner end state, flagged as a follow-up; this
// targeted composition avoids destabilizing the proven top-level container +
// worktree + none paths.)
//
// gitdir-when-mounted (§4a T1.1): a linked worktree's .git is a FILE
// (`gitdir: <main>/.git/worktrees/<name>`), so mounting only the worktree breaks
// git inside the container ("not a git repository"). The fix used here is the
// identical-path .git mirror — the main repo's git common-dir is ALSO bind-mounted
// at its identical host path, so the gitdir pointer resolves and `git status`/
// `git diff`/`git rev-parse` work in-container. This keeps the ENTIRE worktree
// lifecycle host-side (create + WIP-safe teardown via the unchanged Worktree
// machinery); the alternative (T1.1 option b, creating worktrees inside a mounted
// repo) would split that lifecycle across the boundary and put worktree creation +
// teardown out of reach of the host-side WIP-safe Git seam. Identical-path is also
// consistent with the identical-path workspace mount already proven for the
// top-level container.
//
// Approvals bypass (the container is the boundary) and auth (resolveContainerAuth)
// come from the Container half. T1.5 caveat: a low-trust fan-out member currently
// receives the SAME full host creds as the trusted top-level run — per-agent
// key/budget scoping is a later concern, flagged not solved.
type ContainerWorktree struct {
	container Container
	worktree  Worktree
}

// Ensure ContainerWorktree satisfies the Policy interface.
var _ Policy = ContainerWorktree{}

// NewContainerWorktree builds the composition over a container runtime + an
// EXPLICIT image (default profile, no local build — see NewContainer) and a Git
// seam (nil → the default git binary; tests pass a git.Fake to drive the
// worktree lifecycle without a real repo).
func NewContainerWorktree(rt ContainerRuntime, image string, g git.Git) ContainerWorktree {
	return ContainerWorktree{
		container: NewContainer(rt, image),
		worktree:  NewWorktree(g),
	}
}

// NewContainerWorktreeFor builds the composition for a REGISTERED backend name:
// the container half comes from the backend's container profile (image, auth,
// build sources — see NewContainerFor) with the user's image configuration
// applied (image override run as-is / base Containerfile for local builds),
// the worktree half from the Git seam.
func NewContainerWorktreeFor(rt ContainerRuntime, backend string, img ImageConfig, g git.Git) ContainerWorktree {
	return ContainerWorktree{
		container: containerFor(rt, backend, img),
		worktree:  NewWorktree(g),
	}
}

// Name identifies the policy.
func (ContainerWorktree) Name() string { return "container-worktree" }

// Approvals bypasses the in-engine prompt — the container is the boundary (like
// the top-level Container; the worktree half alone would only Prompt).
func (ContainerWorktree) Approvals() Approvals { return ApprovalsBypass }

// PrepareWorkspace provisions a worktree-in-container workspace. The ordering is
// load-bearing for the §2b fan-out degrade chain (container→worktree→none):
//  1. the container gate + host scratch FIRST — if the container can't launch (no
//     runtime/image/auth) it fails BEFORE any worktree is created, so the caller's
//     chain degrades cleanly to a BARE worktree with nothing to unwind;
//  2. then the per-member worktree, reusing Worktree.PrepareWorkspace so the §3.1
//     info/exclude + skip-worktree and the WIP-safe teardown all come for free — a
//     non-git repo fails here and the chain degrades worktree→none;
//  3. then the identical-path .git gitdir mirror mount so git resolves in-container.
//
// Any failure AFTER the worktree exists unwinds both the worktree (WIP-safe) and
// the scratch before returning, so a degrade never leaks a checkout or a temp dir.
func (c ContainerWorktree) PrepareWorkspace(ctx context.Context, projectDir, agentID string) (Workspace, error) {
	sc, err := c.container.prepareContainerScratch(ctx)
	if err != nil {
		return nil, err
	}

	raw, err := c.worktree.PrepareWorkspace(ctx, projectDir, agentID)
	if err != nil {
		_ = os.RemoveAll(sc.root)
		return nil, err
	}
	wt, ok := raw.(*worktreeWorkspace)
	if !ok {
		_ = raw.Cleanup()
		_ = os.RemoveAll(sc.root)
		return nil, fmt.Errorf("container-worktree: unexpected worktree workspace %T", raw)
	}

	gitMount, err := c.gitdirMount(ctx, wt.dir)
	if err != nil {
		// The worktree just failed to yield a usable gitdir mount; tearing it down
		// (WIP-safe — freshly created, so clean) lets the chain retry as a bare
		// host worktree, where git resolves natively (§3, a Tier-0 non-issue).
		_ = wt.Cleanup()
		_ = os.RemoveAll(sc.root)
		return nil, err
	}

	return &containerWorktreeWorkspace{
		wt:          wt,
		scratchRoot: sc.root,
		socketDir:   sc.socketDir,
		extraEnv:    sc.auth.env,
		extraMounts: append(append([]Mount(nil), sc.auth.mounts...), gitMount),
		authMode:    sc.auth.mode,
		agentID:     agentID,
	}, nil
}

// gitdirMount builds the identical-path .git mirror mount (§4a T1.1) from the
// worktree's git common-dir, so the worktree's `gitdir:` pointer resolves inside
// the container. Read-write by design: the worktree's per-checkout admin files
// (index, HEAD) under <common>/worktrees/<name> are written there, exactly as a
// host-native worktree writes to the shared .git — no new blast radius over a bare
// worktree, which also shares the main repo's .git.
func (c ContainerWorktree) gitdirMount(ctx context.Context, worktreeDir string) (Mount, error) {
	common, err := c.worktree.git.CommonDir(ctx, worktreeDir)
	if err != nil {
		return Mount{}, fmt.Errorf("resolve git common dir for container gitdir mount: %w", err)
	}
	return Mount{Host: common, Container: common}, nil
}

// SpawnClient launches the plugin in a container that mounts the member's WORKTREE
// (identical-path) as cwd plus the .git gitdir mirror, delegating to the shared
// Container spawn — a worktree-in-container run is just a container run whose
// workDir is the worktree and whose extra mounts include the gitdir mirror.
func (c ContainerWorktree) SpawnClient(backendName, label string, verbosity int, ws Workspace) (pb.Client, error) {
	cw, ok := ws.(*containerWorktreeWorkspace)
	if !ok {
		return nil, fmt.Errorf("container-worktree spawn: unexpected workspace %T (expected a worktree-in-container workspace)", ws)
	}
	return c.container.spawnInContainer(backendName, label, verbosity, cw.agentID, cw.wt.dir, cw.socketDir, cw.extraEnv, cw.extraMounts, cw.authMode)
}

// containerWorktreeWorkspace composes the per-member worktree (Dir + WIP-safe
// teardown) with the host container scratch. Dir() is the WORKTREE checkout
// (bind-mounted identical-path into the container as cwd). It deliberately does
// NOT implement EnvWorkspace: the engine runs inside the container with a fresh
// HOME, so the worktree's host config-home envs (CLAUDE_CONFIG_DIR/…) would point
// at unmounted host paths and mean nothing there.
type containerWorktreeWorkspace struct {
	wt          *worktreeWorkspace // per-member worktree (Dir + WIP-safe teardown)
	scratchRoot string             // host container scratch (socket dir), removed on cleanup
	socketDir   string             // scratchRoot/sock — go-plugin's unix-socket dir
	extraEnv    []string           // scoped auth env (passthrough)
	extraMounts []Mount            // auth credential mounts + the .git gitdir mirror
	authMode    containerAuthMode
	agentID     string
}

// Ensure the composed workspace satisfies Workspace (and NOT EnvWorkspace).
var _ Workspace = (*containerWorktreeWorkspace)(nil)

// Dir returns the per-member worktree checkout (the container mounts it as cwd).
func (w *containerWorktreeWorkspace) Dir() string { return w.wt.Dir() }

// Cleanup removes the host container scratch FIRST, then runs the worktree's
// WIP-safe, nested-worktree-aware teardown. The caller kills the client (docker
// rm -f the container) BEFORE Cleanup, so the worktree is no longer mounted
// anywhere when it is removed — kill-then-teardown keeps the removal WIP-safe. It
// never returns an error: the scratch removal is best-effort and the worktree
// teardown is itself WIP-safe (an inner worktree with WIP leaves the tree in
// place). Idempotent (the worktree teardown guards on its own cleared dir).
func (w *containerWorktreeWorkspace) Cleanup() error {
	if w.scratchRoot != "" {
		root := w.scratchRoot
		w.scratchRoot = ""
		_ = os.RemoveAll(root)
	}
	return w.wt.Cleanup()
}

package isolation

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/git"
)

// worktreeBase is the worktree-in-container base: the {Workspace: worktree} ×
// {Runtime: container} composition, collapsed from the former ContainerWorktree
// policy into a Container base. The container's cwd is a per-agent git worktree
// (Phase 2) rather than the LIVE project dir, so concurrent members never share a
// checkout AND the engine is contained. It REUSES the two proven halves — the
// Worktree policy's WIP-safe, nested-aware lifecycle (create + excludes/
// skip-worktree + teardown) via the wrapped Worktree, and the Container policy's
// scratch/auth/spawn via the surrounding Container — rather than duplicating
// either.
//
// gitdir-when-mounted: a linked worktree's .git is a FILE
// (`gitdir: <main>/.git/worktrees/<name>`), so mounting only the worktree breaks
// git inside the container ("not a git repository"). The fix is the identical-path
// .git mirror — the main repo's git common-dir is ALSO bind-mounted at its
// identical host path (gitCommonDirMount), so the gitdir pointer resolves and
// `git status`/`git diff`/`git rev-parse` work in-container. This keeps the ENTIRE
// worktree lifecycle host-side (create + WIP-safe teardown via the unchanged
// Worktree machinery); the alternative (creating worktrees inside a mounted repo)
// would split that lifecycle across the boundary and put worktree creation +
// teardown out of reach of the host-side WIP-safe Git seam.
//
// Auth (resolveContainerAuth) comes from the surrounding Container. Trust
// caveat: a low-trust fan-out member
// currently receives the SAME full host creds as the trusted top-level run —
// per-agent key/budget scoping is a later concern, flagged not solved.
type worktreeBase struct{ wt Worktree }

// name identifies the worktree-in-container policy.
func (worktreeBase) name() string { return PolicyNameContainerWorktree }

// withState stamps the run's session identity onto the wrapped Worktree, homing
// its ephemeral per-agent checkout scratch under the session dir (the double-stamp
// alongside Container.state — see withSessionState). Bases are value types, so the
// stamped copy is returned.
func (b worktreeBase) withState(state SessionState) containerBase {
	b.wt.state = state
	return b
}

// prepareBase provisions the per-member worktree-in-container base. The ordering
// is load-bearing for the degrade chain (container→worktree→none) and for a
// leak-free unwind — the Container gate + host scratch already ran (this is called
// only after prepareContainerScratch succeeded):
//  1. the per-member worktree, reusing Worktree.PrepareWorkspace so the
//     info/exclude + skip-worktree and the WIP-safe teardown all come for free — a
//     non-git repo fails HERE (before any resource is created) and the chain
//     degrades worktree→none;
//  2. then the identical-path .git gitdir mirror mount so git resolves in-container.
//
// Any failure AFTER the worktree exists tears the worktree down (WIP-safe — it is
// freshly created, so clean) BEFORE returning, and the caller
// (Container.PrepareWorkspace) then removes the shared scratch — so a degrade never
// leaks a checkout or a temp dir. This is the ONE place a botched collapse could
// leak a checkout, so the unwind order is exact: worktree teardown first (here),
// scratch removal after (the caller).
func (b worktreeBase) resolveBase(ctx context.Context, projectDir, agentID string) (string, func() error, error) {
	raw, err := b.wt.ResolveWorkspace(ctx, projectDir, agentID)
	if err != nil {
		// The worktree never came up (non-git repo / add failed) — nothing created
		// to unwind; the caller removes the shared scratch and the chain degrades.
		return "", nil, err
	}
	wt, ok := raw.(*worktreeWorkspace)
	if !ok {
		// Defensive: an unexpected workspace type. Tear the worktree down
		// (WIP-safe) before failing so nothing leaks.
		_ = raw.Cleanup()
		return "", nil, fmt.Errorf("container-worktree: unexpected worktree workspace %T", raw)
	}
	// cleanup is the worktree's WIP-safe, nested-aware teardown; the container mounts
	// wt.dir as cwd. It deliberately exposes NO per-agent config-home env: the engine
	// runs inside the container with a fresh HOME, so the worktree's host config-home
	// envs (CLAUDE_CONFIG_DIR/…) would point at unmounted host paths and mean nothing
	// there — the unified containerWorkspace never implements EnvWorkspace.
	return wt.dir, wt.Cleanup, nil
}

// mountBase mirrors the checkout's git common dir identical-path. The worktree's
// .git is ALWAYS a pointer file, so the mirror is unconditional (unlike the host
// base's pointer-only mirror). The mapping creates nothing host-side, hence the
// nil cleanup; a failure here leaves the checkout for the workspace to tear down,
// which lets the chain retry as a bare host worktree where git resolves natively
// (a Tier-0 non-issue).
func (b worktreeBase) mountBase(ctx context.Context, rt Runtime, dir, _ string, _ engineContainerSpec, _ git.Git) ([]Mount, func() error, error) {
	gitMount, err := gitCommonDirMount(ctx, rt, b.wt.git, dir)
	if err != nil {
		return nil, nil, err
	}
	return []Mount{gitMount}, nil, nil
}

// NewContainerWorktreeFor builds the worktree-in-container policy for a REGISTERED
// backend name: the container half comes from the backend's container spec
// (image, auth, build sources — see NewContainerFor) with the user's image
// configuration applied (image override run as-is / base Containerfile for local
// builds), the worktree half from the Git seam.
func NewContainerWorktreeFor(rt Runtime, backend string, img ImageConfig, g git.Git) Container {
	c := containerFor(rt, backend, img)
	// backend threaded for consistency with the pure host+worktree
	// construction site (chainFor); harmless here specifically because the
	// unified containerWorkspace never implements EnvWorkspace (see the
	// package doc above), so the wrapped Worktree's Env()/credential-seeding
	// is dead code in this composition — auth flows through the surrounding
	// Container's resolveContainerAuth mounts instead.
	c.base = worktreeBase{wt: NewWorktree(g, backend)}
	return c
}

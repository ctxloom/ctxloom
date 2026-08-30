package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/paths"
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

// resolveBase provisions the per-member worktree-in-container base. The ordering
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

// mountBase mirrors the checkout's git common dir identical-path, and delivers
// the project's config tree into the checkout. The worktree's .git is ALWAYS a
// pointer file, so the git mirror is unconditional (unlike the host base's
// pointer-only mirror). The mapping creates nothing host-side but the config
// mountpoint INSIDE the ephemeral checkout, which dies with it, hence the nil
// cleanup; a failure here leaves the checkout for the workspace to tear down,
// which lets the chain retry as a bare host worktree where git resolves natively
// (a Tier-0 non-issue).
func (b worktreeBase) mountBase(ctx context.Context, rt Runtime, projectDir, dir, _ string, _ engineContainerSpec, _ git.Git) ([]Mount, func() error, error) {
	gitMount, err := gitCommonDirMount(ctx, rt, b.wt.git, dir)
	if err != nil {
		return nil, nil, err
	}
	cfgMount, ok, err := projectConfigMount(rt, projectDir, dir)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return []Mount{gitMount}, nil, nil
	}
	return []Mount{gitMount, cfgMount}, nil, nil
}

// projectConfigMount delivers the LIVE project's .ctxloom tree into a worktree
// cell, read-only, at the cell's own .ctxloom path.
//
// The two sides of that path are NOT the same string, and conflating them is a
// live bug rather than a hypothetical one: the mountpoint is created on the
// HOST, inside the checkout, while the mount TARGET names where the checkout
// appears in the container's namespace — which is mapper().toContainer(dir),
// the same translation Container.Mount applies to the cwd (ExposeMapped). Under
// today's identityMapper the two coincide; under a non-identity mapper, using
// the host path as the target would land the config OUTSIDE the checkout and
// leave the very refusal this function exists to prevent.
//
// WHY THIS EXISTS. A cell's cwd is a fresh `git worktree` checkout, so it holds
// only COMMITTED files. A project whose .ctxloom is gitignored — which is
// ordinary; it holds local state — therefore produces a checkout with no config
// at all, and the ctxloom running INSIDE the container walks up from its cwd,
// finds none, and refuses to launch (config.worktreeSignpost's fatal finding,
// exit 3). The plain container base never hits this because its cwd IS the live
// project, gitignored files included; only the worktree base crosses a boundary
// that drops them. Delivering the tree is what makes the two bases agree.
//
// It also puts worktreeSignpost back inside its own design premise. That check
// speaks to a HUMAN who walked into a linked worktree by hand, and both remedies
// it offers say so ("run ctxloom from the main worktree", "`ctxloom init` here").
// Neither is followable by a cell: the caller ASKED for the worktree, and "here"
// is an ephemeral checkout about to be torn down. A refusal whose remedy the
// reader cannot take is a dead end, so the fix is to stop the premise being
// violated rather than to reword the refusal.
//
// READ-ONLY, deliberately. The worktree axis promises the live project's files
// are not the cell's to change, and a read-write delivery would quietly punch a
// hole straight through that promise into the one tree the user actually keeps.
// Nothing legitimate writes there from a cell: the worktree axis homes its
// instance state under ~/.ctxloom/sessions/<harp>/ephemeral (CopyAmbient's D8
// ruling), not in the project, so a write here would be the bug, not the need.
//
// A checkout carrying its OWN committed .ctxloom is left alone — no mount, no
// shadowing. That is not politeness, it is the same precedence worktreeSignpost
// already implements (its doc: own .ctxloom always wins, no further worktree
// inspection); overriding it would make a deliberately separate project silently
// adopt its parent's config.
func projectConfigMount(rt Runtime, projectDir, worktreeDir string) (Mount, bool, error) {
	hostTarget := filepath.Join(worktreeDir, paths.AppDirName)
	switch _, err := os.Stat(hostTarget); {
	case err == nil:
		return Mount{}, false, nil // the checkout's own config wins
	case !errors.Is(err, os.ErrNotExist):
		// Unreadable is NOT absent: answering "deliver it" would shadow a config
		// that may be there, and answering "skip" would strand the cell without
		// one. Fail so the chain degrades loudly instead of guessing.
		return Mount{}, false, fmt.Errorf("container-worktree: reading %s in the worktree: %w", paths.AppDirName, err)
	}
	source := filepath.Join(projectDir, paths.AppDirName)
	info, err := os.Stat(source)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The project genuinely has no config. Nothing to deliver, and the
		// in-container refusal that follows is then CORRECT and about the
		// project itself, not an artefact of the worktree boundary.
		return Mount{}, false, nil
	case err != nil:
		return Mount{}, false, fmt.Errorf("container-worktree: reading the project %s: %w", paths.AppDirName, err)
	case !info.IsDir():
		return Mount{}, false, nil
	}
	// Pre-create the mountpoint as the invoking user, for the reason
	// containerConfigOverlay spells out: a target the daemon has to create is
	// created as ROOT under a rootful daemon. Harmless in the checkout itself
	// (it is torn down), but it would be a root-owned directory the host-side
	// WIP-safe teardown then cannot remove. Empty and untracked, so git never
	// reports it and the teardown stays WIP-safe.
	if err := os.MkdirAll(hostTarget, 0o755); err != nil {
		return Mount{}, false, fmt.Errorf("container-worktree: creating the %s mountpoint: %w", paths.AppDirName, err)
	}
	containerTarget := filepath.Join(rt.mapper().toContainer(worktreeDir), paths.AppDirName)
	return rt.Expose(source, containerTarget, true), true, nil
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

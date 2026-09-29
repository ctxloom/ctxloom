package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/core/paths"
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
// git inside the container ("not a git repository"). The fix is the .git
// mirror — this checkout's git data is ALSO bind-mounted, where the runtime
// maps it (gitDirMounts) — so the pointers resolve and
// `git status`/`git diff`/`git rev-parse` work in-container. This keeps the ENTIRE
// worktree lifecycle host-side (create + WIP-safe teardown via the unchanged
// Worktree machinery); the alternative (creating worktrees inside a mounted repo)
// would split that lifecycle across the boundary and put worktree creation +
// teardown out of reach of the host-side WIP-safe Git seam.
//
// Auth is the run's own Credentials, as for the surrounding Container: a
// delegated member authenticates exactly as the owner's run does — full
// parity at every delegation depth, no trust gate, by ruling.
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
//  2. then the .git gitdir mirror (and pointer) mounts so git resolves in-container.
//
// Any failure AFTER the worktree exists tears the worktree down (WIP-safe — it is
// freshly created, so clean) BEFORE returning, and the caller
// (Container.PrepareWorkspace) then removes the shared scratch — so a degrade never
// leaks a checkout or a temp dir. This is the ONE place a botched collapse could
// leak a checkout, so the unwind order is exact: worktree teardown first (here),
// scratch removal after (the caller).
func (b worktreeBase) resolveBase(ctx context.Context, projectDir, agentID string) (string, func() error, error) {
	raw, err := b.wt.resolveWorkspace(ctx, projectDir, agentID)
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
	// cleanup is the worktree's WIP-safe, nested-aware teardown; the container
	// mounts wt.dir as cwd. The worktree's own Env() (host scratch dir, git
	// identity) is not carried: the engine runs inside the container, where a
	// host scratch path means nothing — the unified containerWorkspace never
	// implements envWorkspace, and the container's git identity rides its
	// mount plan instead.
	return wt.dir, wt.Cleanup, nil
}

// mountBase mirrors the checkout's own git data (gitDirMounts), and delivers the
// project's config tree into the checkout. The worktree's .git is ALWAYS a
// pointer file, so the git mirror is unconditional (unlike the host base's
// pointer-only mirror). The mapping creates nothing host-side but the config
// mountpoint INSIDE the ephemeral checkout, which dies with it; a failure here leaves the checkout for the workspace to tear down,
// which lets the chain retry as a bare host worktree where git resolves natively
// (a Tier-0 non-issue).
func (b worktreeBase) mountBase(ctx context.Context, rt Runtime, projectDir, dir, scratchRoot string, _ engineContainerSpec, _ git.Git) ([]mount, error) {
	mounts, err := gitDirMounts(ctx, rt, b.wt.git, dir, scratchRoot)
	if err != nil {
		return nil, err
	}
	cfgMount, ok, err := projectConfigMount(rt, projectDir, dir)
	if err != nil {
		return nil, err
	}
	if ok {
		mounts = append(mounts, cfgMount)
	}
	return mounts, nil
}

// projectConfigMount delivers the LIVE project's .ctxloom tree into a worktree
// cell, read-only, at the cell's own .ctxloom path.
//
// The two sides of that path are NOT the same string, and conflating them is a
// live bug rather than a hypothetical one: the mountpoint is created on the
// HOST, inside the checkout, while the mount TARGET names where that
// mountpoint appears in the container's namespace — the runtime's mapping of
// it, the same translation the relocator applies to the cwd (relocateRoot).
// On a POSIX host the two coincide; on Windows, using the host path as the
// target would land the config OUTSIDE the checkout and leave the very
// refusal this function exists to prevent.
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
// Nothing legitimate writes through THIS mount from a cell: the one thing a
// cell does write under the project's .ctxloom — its controlled engine home,
// in the state tier — reaches it through its own read-write mount (the
// session home, relocateRoot), never through the delivered config tree, so a write here
// would be the bug, not the need.
//
// A checkout carrying its OWN committed .ctxloom is left alone — no mount, no
// shadowing. That is not politeness, it is the same precedence worktreeSignpost
// already implements (its doc: own .ctxloom always wins, no further worktree
// inspection); overriding it would make a deliberately separate project silently
// adopt its parent's config.
func projectConfigMount(rt Runtime, projectDir, worktreeDir string) (mount, bool, error) {
	hostTarget := filepath.Join(worktreeDir, paths.AppDirName)
	switch _, err := os.Stat(hostTarget); {
	case err == nil:
		return mount{}, false, nil // the checkout's own config wins
	case !errors.Is(err, os.ErrNotExist):
		// Unreadable is NOT absent: answering "deliver it" would shadow a config
		// that may be there, and answering "skip" would strand the cell without
		// one. Fail so the chain degrades loudly instead of guessing.
		return mount{}, false, fmt.Errorf("container-worktree: reading %s in the worktree: %w", paths.AppDirName, err)
	}
	source := filepath.Join(projectDir, paths.AppDirName)
	info, err := os.Stat(source)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The project genuinely has no config. Nothing to deliver, and the
		// in-container refusal that follows is then CORRECT and about the
		// project itself, not an artefact of the worktree boundary.
		return mount{}, false, nil
	case err != nil:
		return mount{}, false, fmt.Errorf("container-worktree: reading the project %s: %w", paths.AppDirName, err)
	case !info.IsDir():
		return mount{}, false, nil
	}
	// Pre-create the mountpoint as the invoking user, for the reason
	// containerConfigOverlay spells out: a target the daemon has to create is
	// created as ROOT under a rootful daemon. Harmless in the checkout itself
	// (it is torn down), but it would be a root-owned directory the host-side
	// WIP-safe teardown then cannot remove. Empty and untracked, so git never
	// reports it and the teardown stays WIP-safe.
	if err := os.MkdirAll(hostTarget, 0o755); err != nil {
		return mount{}, false, fmt.Errorf("container-worktree: creating the %s mountpoint: %w", paths.AppDirName, err)
	}
	target, err := rt.mapper().toContainer(hostTarget)
	if err != nil {
		return mount{}, false, fmt.Errorf("container-worktree: the checkout's %s has no route into the container: %w", paths.AppDirName, err)
	}
	return rt.expose(source, target, true), true, nil
}

// NewContainerWorktreeFor builds the worktree-in-container policy for a REGISTERED
// backend name: the container half comes from the backend's container spec
// (image, auth, build sources — see NewContainerFor) with the user's image
// configuration applied (image override run as-is / base Containerfile for local
// builds), the worktree half from the Git seam.
func NewContainerWorktreeFor(rt Runtime, backend string, img ImageConfig, g git.Git) Container {
	c := containerFor(rt, backend, img)
	c.base = worktreeBase{wt: NewWorktree(g)}
	return c
}

package projectroot

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// TaskStoreRoot resolves the directory a TASK STORE's project identity
// should key on for dir. It is a NEW, narrower seam than FindRoot/WorkDir:
// those stay worktree-distinct on purpose (task brown-canal, 2026-07-10 —
// see worktreeSignpost in internal/config/config.go), because sessions and
// the runtime coordinator each need their OWN identity per worktree. Tasks
// are different: "tasks aren't context" — an agent working in an ephemeral
// linked worktree that finds something outside its own remit needs to file
// it somewhere the COORDINATOR (running from the primary checkout) will
// actually see, or the finding dies with the worktree. This function is that
// one exception, applied nowhere else.
//
// dir is returned UNCHANGED in the common cases: dir is not a linked
// worktree at all (the main worktree, a plain repo, or no repo); or dir
// carries its own project-id MARKER, which is the opt-out worktreeSignpost
// documents — an explicit `ctxloom init` there makes the worktree a
// deliberately separate project, and that choice must be respected here too
// rather than silently overridden.
//
// The opt-out keys on the MARKER, not on the mere presence of a .ctxloom
// directory, and the difference is load-bearing rather than stylistic. A
// project's .ctxloom is COMMITTED — config.yaml, lock.yaml, profiles/ and
// content/ are tracked "by omission" (see the generated .ctxloom/.gitignore,
// gitignore.PrivateStatePatterns), because they are the content and config the
// project depends on. So `git worktree add` alone materializes a complete
// .ctxloom in every linked worktree, and its presence there says nothing
// whatsoever about intent. Keying the opt-out on it therefore fired for EVERY
// worktree of any project that commits its config: the worktree kept a task
// store of its own, projectid.Resolve found neither a registry entry nor a
// marker, and minted a brand-new project — an empty task log, reported as
// ActionNewProject, which carries no warning by design. Every task the
// coordinator could see simply vanished, and nothing said so.
//
// project-id is the one thing that CANNOT ride the checkout: it is explicitly
// gitignored, precisely because identity is per-tree rather than per-project
// content. That makes it the only signal here able to tell a deliberate
// `ctxloom init` apart from an ordinary checkout, which is exactly the
// question this opt-out is asking.
//
// A stale worktree pointer (info.MainRootExists false — the primary checkout
// moved or was deleted without `git worktree remove`/`prune`) is a hard
// error, never a silent fallback to dir itself: minting or using a task
// store keyed on an orphaned worktree is exactly the silent-no-op failure
// mode this codebase works hardest to avoid — the store would work fine and
// nobody would ever read it again. An empty dir is a hard error for the same
// reason (errNoDir): it would key the store on wherever the process was
// launched.
func TaskStoreRoot(fs afero.Fs, dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("resolve task-store root: %w", errNoDir)
	}
	ownMarker, markerErr := paths.ProjectMarkerPath(dir)
	if markerErr != nil {
		return "", fmt.Errorf("resolve task-store root for %s: %w", dir, markerErr)
	}
	data, ownErr := afero.ReadFile(fs, ownMarker)
	switch {
	// An all-whitespace marker is NOT an opt-out, because projectid.ReadMarker
	// reports it as no marker at all. Diverging here would keep the worktree's
	// own task store on the strength of a marker that resolution then ignores,
	// minting a fresh project anyway — the exact silent fork this predicate
	// exists to prevent.
	case ownErr == nil && strings.TrimSpace(string(data)) != "":
		return dir, nil // opt-out: dir carries its own project identity
	case ownErr != nil && !errors.Is(ownErr, iofs.ErrNotExist):
		// Only plain absence means "no opt-out here". A permission or I/O
		// fault that read as absence would silently redirect this project's
		// task store elsewhere, discarding an opt-out that may well exist.
		return "", fmt.Errorf("resolve task-store root for %s: read %s: %w", dir, ownMarker, ownErr)
	}

	info, err := DetectWorktree(fs, dir)
	if err != nil {
		return "", fmt.Errorf("resolve task-store root for %s: %w", dir, err)
	}
	if !info.Linked {
		return dir, nil
	}
	if !info.MainRootExists {
		return "", fmt.Errorf(
			"%s is a linked git worktree, but its primary checkout at %s is missing or unreadable (stale worktree pointer); "+
				"restore it, run `git worktree prune` from a healthy checkout, or run `ctxloom init` here to make this worktree's task store deliberately separate",
			dir, info.MainRoot)
	}
	return info.MainRoot, nil
}

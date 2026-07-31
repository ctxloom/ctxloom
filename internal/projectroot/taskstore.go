package projectroot

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"

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
// already carries its own .ctxloom, which is the same opt-out
// worktreeSignpost documents — an explicit `ctxloom init` there makes the
// worktree a deliberately separate project, and that choice must be
// respected here too rather than silently overridden.
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
	ownAppDir := filepath.Join(dir, paths.AppDirName)
	ownInfo, ownErr := fs.Stat(ownAppDir)
	switch {
	case ownErr == nil && ownInfo.IsDir():
		return dir, nil // opt-out: dir is a deliberately separate project
	case ownErr != nil && !errors.Is(ownErr, iofs.ErrNotExist):
		// Only plain absence means "no opt-out here". A permission or I/O
		// fault that read as absence would silently redirect this project's
		// task store elsewhere, discarding an opt-out that may well exist.
		return "", fmt.Errorf("resolve task-store root for %s: stat %s: %w", dir, ownAppDir, ownErr)
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

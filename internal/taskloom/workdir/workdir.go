// Package workdir resolves the project work root the same way `ctxloom run`
// does, so a task added from a repo subdirectory lands in the same project log
// the session uses:
//
//	CTXLOOM_ROOT (valid) -> git root -> cwd -> "."
//
// CTXLOOM_ROOT is purely an override at the top of the chain. When unset it
// changes nothing; when set but invalid (missing path or not a directory) it
// warns once per process and falls through as if unset — a bad override never
// blocks a task operation.
//
// The resolved root is then redirected through projectroot.TaskStoreRoot: a
// linked git worktree with no .ctxloom of its own resolves to its PRIMARY
// checkout instead, so the task store — and only the task store — is shared
// across a worktree and the project it was cut from. See ResolveBoundary.
package workdir

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/projectroot"
)

// Resolve returns the project work root. Resolution delegates entirely to
// projectroot.WorkDirWithBoundary (U140-F01 — this package no longer keeps
// its own copy of the env var name, the env-reading step, or the
// git-root/cwd chain).
func Resolve() (string, error) {
	root, _, err := ResolveBoundary()
	return root, err
}

// ResolveBoundary is Resolve, but also reports whether a genuine project
// boundary was found — a CTXLOOM_ROOT override or an enclosing git repository
// — as opposed to falling all the way through to the bare working directory.
// Minting a project identity is fine for a real boundary (including a git
// repo's first-ever taskloom call); it is not fine for "whatever directory
// the shell happened to be in", which is what a bare-cwd fallback means. The
// task-listing default-scope logic uses this distinction to decide whether a
// read may need to fall back to aggregating every project instead of minting
// a fresh, throwaway identity for an arbitrary directory (see ADR 0025 and
// resolveListScope in cmd/taskloom).
//
// The resolved boundary is then passed through projectroot.TaskStoreRoot: a
// linked git worktree with no .ctxloom of its own redirects to its PRIMARY
// checkout root, so a task filed from an ephemeral worktree lands in the log
// the coordinator (running from the primary checkout) actually reads,
// instead of a store that dies with the worktree. This is scoped to the
// task store alone — "tasks aren't context" — and does not change what
// boundary was FOUND, only which directory's identity the task store keys
// on once one was. A worktree with its own .ctxloom (an explicit `ctxloom
// init` there) opts out and is never redirected. A stale worktree pointer
// (the primary checkout is gone) is a hard error: this package never falls
// back to minting a task store nobody will find again.
func ResolveBoundary() (root string, found bool, err error) {
	base, found, berr := projectroot.WorkDirWithBoundary()
	if berr != nil {
		// A failing os.Getwd (the only source of this error — see
		// projectroot.WorkDirWithBoundary) is a hard error here, never
		// silently treated as "." (U140-F02 — that value was invented by
		// this package's OWN prior copy of this chain, and could be minted
		// as a permanent project-registry identity keyed on "wherever any
		// future process happens to be").
		return "", false, fmt.Errorf("resolve project work root: %w", berr)
	}
	target, terr := projectroot.TaskStoreRoot(afero.NewOsFs(), base)
	if terr != nil {
		return "", found, fmt.Errorf("resolve task-store root: %w", terr)
	}
	return target, found, nil
}

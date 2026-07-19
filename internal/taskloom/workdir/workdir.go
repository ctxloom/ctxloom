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
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// EnvVar is the project-root override variable, shared with ctxloom.
const EnvVar = "CTXLOOM_ROOT"

var warnOnce sync.Once

// Resolve returns the project work root.
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
	base, found := resolveBase()
	target, terr := projectroot.TaskStoreRoot(afero.NewOsFs(), base)
	if terr != nil {
		return "", found, fmt.Errorf("resolve task-store root: %w", terr)
	}
	return target, found, nil
}

// resolveBase is ResolveBoundary's chain before the task-store worktree
// redirect: CTXLOOM_ROOT override, else the enclosing git repository root,
// else the bare working directory.
func resolveBase() (root string, found bool) {
	if r, ok := fromEnv(); ok {
		return r, true
	}
	if r, ok := gitRoot(); ok {
		return r, true
	}
	if wd, err := os.Getwd(); err == nil {
		return wd, false
	}
	return ".", false
}

// fromEnv returns (root, true) when CTXLOOM_ROOT is set and names an existing
// directory, where root is its cleaned absolute path. When set but invalid it
// warns to stderr once per process and returns ("", false). When unset it
// returns ("", false) with no warning.
func fromEnv() (string, bool) {
	raw, set := os.LookupEnv(EnvVar)
	if !set || raw == "" {
		return "", false
	}
	abs, err := filepath.Abs(raw)
	if err == nil {
		abs = filepath.Clean(abs)
		if info, statErr := os.Stat(abs); statErr == nil && info.IsDir() {
			return abs, true
		}
	}
	warnOnce.Do(func() {
		clidiag.Warn("taskloom", "%s=%q is not a valid directory; ignoring it and falling back to git root / current directory", EnvVar, raw)
	})
	return "", false
}

// gitRoot resolves the enclosing git repository root (worktrees and submodules
// included) via go-git, returning ("", false) when cwd isn't inside a repo.
func gitRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	root, err := gitutil.FindRoot(dir)
	if err != nil {
		return "", false
	}
	return root, true
}

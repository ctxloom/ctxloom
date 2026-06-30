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
package workdir

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// EnvVar is the project-root override variable, shared with ctxloom.
const EnvVar = "CTXLOOM_ROOT"

var warnOnce sync.Once

// Resolve returns the project work root.
func Resolve() string {
	if root, ok := fromEnv(); ok {
		return root
	}
	if root, ok := gitRoot(); ok {
		return root
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
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

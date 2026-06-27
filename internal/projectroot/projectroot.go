// Package projectroot resolves the authoritative project root, honoring the
// CTXLOOM_ROOT override above git-root detection and cwd traversal.
//
// CTXLOOM_ROOT is purely an override at the top of ctxloom's existing
// resolution chain (git root -> cwd walk-up -> home). When the variable is
// unset it changes nothing and the prior mechanisms apply byte-for-byte. When
// set and valid it short-circuits discovery. When set but invalid (missing path
// or not a directory) it warns once per process and falls through as if unset,
// per the fault-tolerance philosophy — a bad override never blocks startup.
package projectroot

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/shared/clidiag"
	"github.com/ctxloom/shared/gitutil"
)

// EnvVar is the project-root override variable. Documented in docs/environment.md
// and listed in testsupport.EnvKeys so test isolation clears it.
const EnvVar = "CTXLOOM_ROOT"

var warnOnce sync.Once

// resolve is the pure resolution of CTXLOOM_ROOT, with no side effects:
//   - unset/empty           -> ("", false, "")
//   - set but invalid       -> ("", false, raw)   raw is the offending value
//   - set and a valid dir   -> (cleanedAbs, true, "")
//
// The directory check runs against fs so it shares the filesystem the caller
// uses for the rest of resolution — production passes the OS fs, tests pass an
// afero mem fs and stay off real disk. A relative value is anchored to the
// launching cwd via filepath.Abs so the override resolves predictably.
func resolve(fs afero.Fs) (root string, ok bool, rawInvalid string) {
	raw, set := os.LookupEnv(EnvVar)
	if !set || raw == "" {
		return "", false, ""
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", false, raw
	}
	abs = filepath.Clean(abs)
	if info, err := fs.Stat(abs); err != nil || !info.IsDir() {
		return "", false, raw
	}
	return abs, true, ""
}

// FromEnv returns (root, true) when CTXLOOM_ROOT is set and names an existing
// directory on fs, where root is its cleaned absolute path. When the variable
// is set but invalid it warns to stderr once per process and returns
// ("", false). When unset it returns ("", false) with no warning. Callers that
// thread an afero fs (e.g. config) pass it so validation shares their fs;
// process-level callers pass afero.NewOsFs().
func FromEnv(fs afero.Fs) (string, bool) {
	root, ok, rawInvalid := resolve(fs)
	if rawInvalid != "" {
		warnOnce.Do(func() {
			clidiag.Warn("ctxloom", "%s=%q is not a valid directory; ignoring it and falling back to git root / current directory", EnvVar, rawInvalid)
		})
	}
	return root, ok
}

// WorkDir resolves the project work root for sessions and project identity,
// honoring the override above git-root detection:
//
//	CTXLOOM_ROOT (valid) -> git root -> cwd -> "."
//
// It replaces the inline gitutil.FindRoot(".")/cwd blocks so the override is
// applied consistently at every site. It operates on the OS filesystem, since
// git-root detection and cwd resolution are inherently process-level.
func WorkDir() string {
	if root, ok := FromEnv(afero.NewOsFs()); ok {
		return root
	}
	if root, err := gitutil.FindRoot("."); err == nil {
		return root
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return "."
}

// RootFromFallback reports whether WorkDir resolved to the bare cwd fallback —
// no valid CTXLOOM_ROOT override and not inside a git repository. `ctxloom run`
// warns in this case: a cwd-rooted project keys its identity, and with it the
// tasks, plans, and sessions under ~/.ctxloom, off the launch directory rather
// than a stable repo root. They neither follow the directory if it moves nor
// resume from a launch one level up or down. It mirrors WorkDir's branches and,
// like WorkDir, runs against the OS filesystem and real git/cwd detection.
func RootFromFallback() bool {
	if _, ok := FromEnv(afero.NewOsFs()); ok {
		return false
	}
	if _, err := gitutil.FindRoot("."); err == nil {
		return false
	}
	return true
}

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
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
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

// WorkDirWithBoundary resolves the project root — CTXLOOM_ROOT override, else
// the enclosing git repository root, else the bare current working directory
// — and also reports whether that root came from a genuine boundary (an
// override or a discovered git repo) as opposed to falling all the way
// through to the bare cwd (found=false; see RootFromFallback). This is the
// ONE implementation of that three-step chain: WorkDir, RootFromFallback, and
// internal/taskloom/workdir.ResolveBoundary all resolve to it rather than
// each carrying their own copy (U140-F01 — three separate copies, one of
// which additionally duplicated the env-reading step verbatim, warning
// string included, so a bad override could warn twice in one process).
//
// Unlike the three prior copies, a failing os.Getwd is a returned error, not
// silently treated as "." (U140-F02): "." is a directory name meaning
// "wherever any future process happens to be", and minting a project
// identity keyed on it (as internal/shared/tasks/projectid's registry would)
// lets two completely unrelated projects collide onto one task log the
// moment either one's cwd is unavailable — exactly the situation (a reaped
// worktree) this package's callers most need to fail loud in, not paper over.
func WorkDirWithBoundary() (root string, found bool, err error) {
	if r, ok := FromEnv(afero.NewOsFs()); ok {
		return r, true, nil
	}
	if r, gerr := gitutil.FindRoot("."); gerr == nil {
		return r, true, nil
	}
	cwd, gerr := os.Getwd()
	if gerr != nil {
		return "", false, fmt.Errorf("resolve project root: working directory unavailable: %w", gerr)
	}
	return cwd, false, nil
}

// WorkDir resolves the project work root for sessions and project identity,
// honoring the override above git-root detection:
//
//	CTXLOOM_ROOT (valid) -> git root -> cwd -> "."
//
// It operates on the OS filesystem, since git-root detection and cwd
// resolution are inherently process-level. See WorkDirWithBoundary for the
// underlying chain and RootFromFallback for the boundary-found signal.

func WorkDir() string {
	root, _, err := WorkDirWithBoundary()
	if err != nil {
		// WorkDir's public contract predates a possible error (an unlinked
		// cwd — os.Getwd failing — is the only source): preserve its exact
		// legacy behavior for every existing caller rather than changing
		// this function's signature. WorkDirWithBoundary is the one that
		// surfaces the error to a caller equipped to act on it (today,
		// internal/taskloom/workdir.ResolveBoundary).
		return "."
	}
	return root
}

// RootFromFallback reports whether WorkDir resolved to the bare cwd fallback —
// no valid CTXLOOM_ROOT override and not inside a git repository. `ctxloom run`
// warns in this case: a cwd-rooted project keys its identity, and with it the
// tasks, plans, and sessions under ~/.ctxloom, off the launch directory rather
// than a stable repo root. They neither follow the directory if it moves nor
// resume from a launch one level up or down. It mirrors WorkDir's branches and,
// like WorkDir, runs against the OS filesystem and real git/cwd detection.
func RootFromFallback() bool {
	_, found, err := WorkDirWithBoundary()
	if err != nil {
		// An unresolvable working directory is, if anything, MORE of a
		// fallback situation than a bare cwd — callers use this boolean
		// only to decide whether to warn, so erring toward "yes, warn" is
		// the safe default.
		return true
	}
	return !found
}

// Package projectroot answers "which directory is this project rooted at?" for
// every consumer that needs a stable answer. It carries three responsibilities,
// and a reader should be able to find all three from here.
//
// # Root resolution
//
// WorkDir resolves the project work root through CTXLOOM_ROOT -> git root ->
// cwd. WorkDirWithBoundary is the same chain, additionally reporting whether
// the answer came from a real boundary (override or git repo) rather than the
// bare cwd; RootFromFallback exposes just that boolean, which `ctxloom run`
// warns on because a cwd-keyed project identity neither follows the directory
// nor resumes from one level up. FromEnv is the CTXLOOM_ROOT step alone, for
// callers (config) that thread their own afero.Fs.
//
// # Git worktree classification
//
// DetectWorktree inspects a directory's `.git` entry and reports, as a
// WorktreeInfo, whether it is the root of a LINKED worktree, and if so where
// its primary checkout is and whether that checkout still exists. It reads the
// repository layout directly rather than shelling out to git, which is what
// lets it tell a linked worktree apart from a submodule.
//
// # Task-store redirect
//
// TaskStoreRoot is a deliberately narrower seam than WorkDir: it redirects a
// LINKED worktree's task-store identity to the primary checkout, so a finding
// filed by an agent in an ephemeral worktree is still visible to a coordinator
// running from the main tree. Root resolution stays worktree-DISTINCT; this one
// exception exists because "tasks aren't context", and it is applied nowhere
// else. See its own doc for the opt-out and the stale-pointer hard error.
//
// # The CTXLOOM_ROOT override
//
// CTXLOOM_ROOT is purely an override at the top of ctxloom's existing
// resolution chain (git root -> cwd walk-up -> home). When the variable is
// unset it changes nothing and the prior mechanisms apply byte-for-byte. When
// set and valid it short-circuits discovery. When set but invalid (missing path
// or not a directory) it warns once per offending VALUE and falls through as if
// unset, per the fault-tolerance philosophy — a bad override never blocks
// startup.
package projectroot

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// EnvVar is the project-root override variable. Documented in docs/environment.md
// and listed in testsupport.EnvKeys so test isolation clears it.
const EnvVar = "CTXLOOM_ROOT"

// resolve is the pure resolution of CTXLOOM_ROOT, with no side effects:
//   - unset/empty           -> ("", false, "")
//   - set but invalid       -> ("", false, raw)   raw is the offending value
//   - set and a valid dir   -> (cleanedAbs, true, "")
//
// The directory check runs against fs so it shares the filesystem the caller
// uses for the rest of resolution — production passes the OS fs, tests pass an
// afero mem fs and stay off real disk. A relative value is anchored to the
// launching cwd via filepath.Abs so the override resolves predictably.
//
// Those are deliberately two different filesystems, and the split is the
// contract rather than an oversight: fs decides whether the root EXISTS, while
// the anchor for a relative value is always the launching process's cwd, never
// the injected fs's root. CTXLOOM_ROOT is operator-facing, so its relative form
// can only mean "relative to where ctxloom was launched" — reading it against
// an injected fs would make one value name different directories in-process
// and out. filepath.Abs consults the process cwd, not the filesystem, so a mem
// fs caller still reads no bytes from disk.
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
// is set but invalid it warns to stderr once per OFFENDING VALUE and returns
// ("", false). When unset it returns ("", false) with no warning. Callers that
// thread an afero fs (e.g. config) pass it so validation shares their fs;
// process-level callers pass afero.NewOsFs().
//
// The suppression is per-message (clidiag.WarnOnce), not per-process: config
// resolution runs many times in one invocation and dozens more in a long-lived
// server, so an unchanged bad value must collapse to a single line — but a
// DIFFERENT bad value is a different fault and must still be reported. A latch
// keyed on nothing mutes every fault after the first, and nobody is ever told
// about the misconfiguration again.
func FromEnv(fs afero.Fs) (string, bool) {
	root, ok, rawInvalid := resolve(fs)
	if rawInvalid != "" {
		clidiag.WarnOnce("ctxloom", "%s=%q is not a valid directory; ignoring it and falling back to git root / current directory", EnvVar, rawInvalid)
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
// Only "this directory is not inside a repository" is a silent fall-through.
// Every other git failure — a .git that exists and cannot be read, a path
// that will not stat — produces the same cwd fallback but says so first:
// keying a project's identity on the launch directory is the correct
// behaviour when there genuinely is no repository, and a fault worth
// reporting when there is one the process simply could not use.
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
	r, ferr := gitutil.FindRoot(".")
	switch {
	case ferr == nil:
		return r, true, nil
	case !gitutil.IsNoRepository(ferr):
		clidiag.WarnOnce("ctxloom", "git repository detection failed (%v); continuing from the current directory, so this project's identity — its task log, plans and sessions — is keyed on wherever this was launched instead of on a repository root", ferr)
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

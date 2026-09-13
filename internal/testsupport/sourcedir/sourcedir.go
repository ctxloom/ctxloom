// Package sourcedir locates a test binary's own source tree without embedding
// an absolute compile-time path, so the test path can be compiled with
// -trimpath.
//
// WHY NOT runtime.Caller. The idiom this package replaces was
// `_, file, _, _ := runtime.Caller(0); filepath.Dir(file)`. That works only
// because, without -trimpath, the compiler records each file's ABSOLUTE path
// in the binary. Under -trimpath it records the MODULE-RELATIVE path instead
// ("github.com/ctxloom/ctxloom/internal/cli/main_test.go"), so filepath.Dir
// yields a directory that does not exist and every read through it fails with
// "no such file or directory". Those absolute paths are also part of the
// compiler's action ID, which is why identical source checked out in two
// worktrees cannot share a single build-cache entry (obtuse-equinox).
//
// WHY NOT os.Getwd AT CALL TIME. `go test` starts a test binary with its
// working directory set to the package's source directory, so "." is the
// package dir — but only until something moves it. testsupport.SandboxedMain
// chdirs the whole process into a throwaway sandbox before any test runs,
// precisely so ctxloom's app-directory walk-up cannot escape into the
// developer's real ~/.ctxloom. Packages using it were deliberately migrated
// AWAY from cwd-relative paths for that reason. A source scan rooted at "."
// in such a package walks an empty temp directory, matches nothing, reports no
// violations and exits 0 — a gate that evaporates instead of failing, which is
// the exact defect most of these call sites exist to prevent.
//
// WHAT THIS DOES INSTEAD. The working directory is captured ONCE, in this
// package's initialization, which runs before any test function, before any
// TestMain body, and therefore before SandboxedMain's chdir. That value is a
// RUNTIME fact about how the binary was started, so -trimpath does not touch
// it, and it is unaffected by a later chdir. It is equally correct in
// sandboxed and unsandboxed packages.
//
// FAIL LOUD, NEVER VACUOUS. The captured directory is validated at
// initialization: it must contain at least one .go file. A caller that somehow
// starts outside its package directory — a re-exec'd child that inherited a
// sandbox cwd, say — gets a loud error rather than a scan of an empty tree
// that passes by finding nothing.
package sourcedir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// startDir is the process's working directory as of this package's
// initialization: for a `go test` binary, the source directory of the package
// under test. startErr records why it could not be established, so the failure
// surfaces at the call that depends on it rather than as a panic during the
// initialization of an unrelated package.
var startDir, startErr = captureStartDir()

func captureStartDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("capture the test binary's starting working directory: %w", err)
	}
	entries, err := os.ReadDir(wd)
	if err != nil {
		return "", fmt.Errorf("read the test binary's starting working directory %s: %w", wd, err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return wd, nil
		}
	}
	return "", fmt.Errorf("the test binary started in %s, which holds no .go files, so it is not a "+
		"package source directory; anything resolved against it would scan the wrong tree", wd)
}

// Dir returns the source directory of the package under test.
func Dir() (string, error) {
	if startErr != nil {
		return "", startErr
	}
	return startDir, nil
}

// MustDir is Dir for a caller that has nowhere to return an error — a
// package-level variable initializer, or a helper whose whole purpose is the
// path. It panics rather than returning a wrong directory, because every
// caller here reads or scans through the result and a silently wrong root is
// the failure mode this package exists to prevent.
func MustDir() string {
	dir, err := Dir()
	if err != nil {
		panic("sourcedir: " + err.Error())
	}
	return dir
}

// Path joins rel onto the package source directory.
func Path(rel ...string) string {
	return filepath.Join(append([]string{MustDir()}, rel...)...)
}

var (
	repoRootOnce sync.Once
	repoRoot     string
	repoRootErr  error
)

// RepoRoot returns the module root, found by walking up from the package
// source directory for go.mod.
//
// Walking up is deliberate, rather than the filepath.Dir chain the call sites
// used to spell out: a helper that hard-codes "three levels up" is silently
// wrong the moment its package moves, and wrong in the direction that makes a
// source scan read some unrelated directory rather than fail.
func RepoRoot() (string, error) {
	repoRootOnce.Do(func() {
		start, err := Dir()
		if err != nil {
			repoRootErr = err
			return
		}
		for dir := start; ; {
			if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
				repoRoot = dir
				return
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				repoRootErr = fmt.Errorf("no go.mod in %s or any parent, so the module root is unknown", start)
				return
			}
			dir = parent
		}
	})
	if repoRootErr != nil {
		return "", repoRootErr
	}
	return repoRoot, nil
}

// MustRepoRoot is RepoRoot for a caller with nowhere to return an error. It
// panics for the reason MustDir does.
func MustRepoRoot() string {
	root, err := RepoRoot()
	if err != nil {
		panic("sourcedir: " + err.Error())
	}
	return root
}

// RepoPath joins rel onto the module root.
func RepoPath(rel ...string) string {
	return filepath.Join(append([]string{MustRepoRoot()}, rel...)...)
}

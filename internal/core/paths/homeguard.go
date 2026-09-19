package paths

import (
	"flag"
	"fmt"
	"os"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

var (
	homeRecordsMu       sync.RWMutex
	homeRecordsOverride string
)

// SetHomeRecordsDirForTesting points the §9.7 application-record store at dir
// until the returned func is called (wire it to t.Cleanup). It is the seam an
// in-process test uses to keep a record it causes out of the developer's real
// home.
//
// Prefer it to moving $HOME, for the reason its approvals sibling gives: $HOME
// is a whole-machine lever that also moves the config layer, the trust root and
// the session store, and this package's own binary discovery reads it. This
// redirects exactly the store the record lands in.
//
// It follows selfexec.SetPathForTesting's shape — package-level value, guarded
// by a mutex, restored through the returned func — because that is how this
// codebase already spells "a production resolution a test may redirect".
func SetHomeRecordsDirForTesting(dir string) func() {
	homeRecordsMu.Lock()
	prev := homeRecordsOverride
	homeRecordsOverride = dir
	homeRecordsMu.Unlock()
	return func() {
		homeRecordsMu.Lock()
		homeRecordsOverride = prev
		homeRecordsMu.Unlock()
	}
}

// The home-rooted-store guard lives HERE, in the package that resolves every
// home-rooted path, rather than beside any one store. It began beside the
// approvals store, which needed it first; the §9.7 application-record store
// then repeated the mistake it exists to prevent, and 1046 records written by
// test binaries were found in a developer's real ~/.ctxloom/records. A guard
// that protects only the store whose author remembered it is the same shape as
// the per-test helper its own doc rejects.

// UnsandboxedHomeError is the belt to an override's braces: under a TEST
// BINARY, a home-rooted store outside every recognized temp root is refused
// rather than returned. In a production binary it is always nil — the real
// ~/.ctxloom is exactly where real state belongs.
//
// Why refuse instead of silently redirecting: a redirect would make the test
// pass while leaving the mistake in place, and the next home-rooted store
// would repeat it. Refusing names the fix at the moment the fix is cheap.
//
// The containment test mirrors testsupport.appDirIsolationError's HOME arm —
// "is this inside a recognized temp root" — deliberately as the same weak,
// stable fact rather than a prediction of which directory a harness will
// mint. See testTempRoots for why there are two roots rather than one.
func UnsandboxedHomeError(what, dir, remedy string) error {
	if !runningUnderGoTest() {
		return nil
	}
	roots := testTempRoots()
	for _, root := range roots {
		if realpath.Under(dir, root) {
			return nil
		}
	}
	return fmt.Errorf(
		"REFUSING to use the %s at %q from a test binary: it is outside every recognized temp root %v, so anything recorded there "+
			"would land in the developer's real home and outlive this run. Isolate the test — %s",
		what, dir, roots, remedy)
}

// testTempRoots returns every root a test binary may legitimately place a
// sandboxed HOME or store under. There are two, independently, in live use
// across this repo, and a check pinned to only one of them silently
// misjudges paths built by the other:
//
//   - os.TempDir() itself — what a TestMain-style sandbox mkdirs into
//     directly via os.MkdirTemp("", ...) (testsupport.enterSandbox,
//     internal/adapters/operations' own acquireSandbox), independent of the go tool
//     and unaffected by GOTMPDIR.
//   - GOTMPDIR, when set — what testing.T.TempDir() actually allocates
//     under. go1.26.8's testing.(*common).makeTempDir builds a test's temp
//     directory via os.MkdirTemp(os.Getenv("GOTMPDIR"), pattern): it reads
//     GOTMPDIR directly and bypasses os.TempDir() (and the TMPDIR it
//     honours) entirely whenever GOTMPDIR is set. This project's justfile
//     sets GOTMPDIR=/var/tmp/ctxloom-gotmp on purpose — a tmpfs /tmp
//     ENOSPCs the linker under this suite's parallel builds — so a check
//     pinned to os.TempDir() alone silently checks a t.TempDir()-derived
//     path against the wrong root the moment that export takes effect.
//
// This is not a guess at where t.TempDir() lands: os.MkdirTemp's own
// documented behavior for an empty dir argument is to fall back to
// os.TempDir(), so reading GOTMPDIR-or-os.TempDir() here is the exact same
// two-step resolution makeTempDir performs.
func testTempRoots() []string {
	roots := []string{os.TempDir()}
	if v := os.Getenv("GOTMPDIR"); v != "" {
		roots = append(roots, v)
	}
	return roots
}

// runningUnderGoTest reports whether this process is a `go test` binary.
//
// It asks the flag set rather than importing "testing": testing.Init registers
// the test.* flags on flag.CommandLine before any test runs, and nothing else
// does, so the lookup is exact. Importing "testing" from shipped code would
// link the whole test harness — its regexp matcher, its profiler hooks — into
// the ctxloom binary, which is the cost internal/shared/archlint's TestSupportAnalyzer
// exists to keep out.
func runningUnderGoTest() bool { return flag.Lookup("test.v") != nil }

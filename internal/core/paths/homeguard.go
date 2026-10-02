package paths

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/realpath"
)

// homeOverride is a home-rooted directory a test may redirect: a
// package-level value, guarded by a mutex, restored through the returned func
// — selfexec.SetPathForTesting's shape, which is how this codebase already
// spells "a production resolution a test may redirect".
type homeOverride struct {
	mu  sync.RWMutex
	dir string
}

func (o *homeOverride) set(dir string) func() {
	o.mu.Lock()
	prev := o.dir
	o.dir = dir
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		o.dir = prev
		o.mu.Unlock()
	}
}

func (o *homeOverride) get() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.dir
}

var homeRecordsOverride, homeLocksOverride homeOverride

// SetHomeRecordsDirForTesting points the §9.7 application-record store at dir
// until the returned func is called (wire it to t.Cleanup). It is the seam an
// in-process test uses to keep a record it causes out of the developer's real
// home.
//
// Prefer it to moving $HOME, for the reason its approvals sibling gives: $HOME
// is a whole-machine lever that also moves the config layer, the trust root and
// the session store, and this package's own binary discovery reads it. This
// redirects exactly the store the record lands in.
func SetHomeRecordsDirForTesting(dir string) func() { return homeRecordsOverride.set(dir) }

// SetHomeLocksDirForTesting points the home lock directory (HomeLocksDir, and
// so every HomePathFor lock) at dir until the returned func is called. It is
// SetHomeRecordsDirForTesting's sibling, for an in-process test that keeps the
// real $HOME on purpose and must still keep its lock files out of it.
func SetHomeLocksDirForTesting(dir string) func() { return homeLocksOverride.set(dir) }

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

// accountHomeError is UnsandboxedHomeError narrowed to the ACCOUNT's real
// home: it refuses only a dir that sits under the home the passwd database
// gives this user, which does not move with $HOME and so names the
// developer's state even inside a test that rebinds HOME.
//
// It exists for a resolver whose result a test may legitimately DERIVE for a
// home that is not this machine's: the container mount builder computes where
// a container's own HomePathFor looks (HOME=/home/ctxloom), and nothing is
// written by computing it. Refusing every non-temp path refused that
// derivation, not a write.
//
// An account whose home cannot be looked up falls back to the full
// temp-root check, so the guard never becomes weaker than UnsandboxedHomeError
// for want of an answer.
func accountHomeError(what, dir, remedy string) error {
	if u, err := user.Current(); err == nil && u.HomeDir != "" && !realpath.Under(dir, u.HomeDir) {
		return nil
	}
	return UnsandboxedHomeError(what, dir, remedy)
}

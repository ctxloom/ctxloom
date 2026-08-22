package operations

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// The USER (home-scoped) countersignature store is the one piece of ctxloom
// state a plain `ctxloom bundle reject` writes OUTSIDE any project: it lives
// at ~/.ctxloom/approvals, it is shared by every project on the machine, and
// it OUTLIVES the process that wrote it. That makes it the single most
// dangerous location this package resolves, and until this file existed the
// two production sites that needed it — resolveCountersignStore (the writer,
// under SetItemTrust/SetBlacklist) and buildCountersignRecords (the reader,
// under every trust evaluation) — each called paths.HomeApprovalsPath()
// directly, with no seam between them and $HOME.
//
// The consequence was measured, not theorised: a test recording an unsigned
// rejection wrote a real, durable decision into the developer's own
// ~/.ctxloom/approvals, and that decision then withheld content for every
// later test in the package. A per-test helper that moves $HOME fixes the ONE
// test that remembers to call it; it cannot fix the test written next week.
// So the seam is here, in the production resolution path, where it governs
// every caller that exists and every caller that does not exist yet.

var (
	homeApprovalsMu       sync.RWMutex
	homeApprovalsOverride string
)

// SetHomeApprovalsDirForTesting points the USER countersignature store at dir
// until the returned func is called (wire it to t.Cleanup). It is the seam a
// test OUTSIDE this package uses to scope a home-scoped approve/reject to a
// directory of its own.
//
// Prefer it to moving $HOME. $HOME is a process-wide, whole-machine lever: it
// redirects the home config layer, the trust root, the session store, the
// trigger cache and the Go toolchain's own caches along with the approvals
// store, so a test that only wanted to record a rejection somewhere harmless
// pays for all of it and any of it can surprise the next reader. This
// override moves exactly the store the decision lands in, and nothing else.
//
// It follows selfexec.SetPathForTesting's shape — package-level value, guarded
// by a mutex, restored through the returned func — because that is how this
// codebase already spells "a production resolution a test may redirect".
func SetHomeApprovalsDirForTesting(dir string) func() {
	homeApprovalsMu.Lock()
	prev := homeApprovalsOverride
	homeApprovalsOverride = dir
	homeApprovalsMu.Unlock()
	return func() {
		homeApprovalsMu.Lock()
		homeApprovalsOverride = prev
		homeApprovalsMu.Unlock()
	}
}

// homeApprovalsDir is the ONE place this package resolves the USER
// countersignature store. An injected override wins outright; otherwise the
// real ~/.ctxloom/approvals, subject to the test-binary guard below.
//
// Both the writer and the reader come through here deliberately. They must
// agree on WHICH store a personal decision lives in — a writer and a reader
// pointed at different directories is a recorded rejection nothing honours,
// which is the silent no-op this codebase's trust plumbing has been bitten by
// before (see buildCountersignRecords' own note on an unresolvable home).
func homeApprovalsDir() (string, error) {
	homeApprovalsMu.RLock()
	override := homeApprovalsOverride
	homeApprovalsMu.RUnlock()
	if override != "" {
		return override, nil
	}
	dir, err := paths.HomeApprovalsPath()
	if err != nil {
		return "", err
	}
	if err := unsandboxedHomeError("user countersignature store", dir,
		"operations.SetHomeApprovalsDirForTesting(t.TempDir()), or an explicit UserStore on the request"); err != nil {
		return "", err
	}
	return dir, nil
}

// homeAllowedSignersPath is the sibling chokepoint for the OTHER home-rooted
// store this package WRITES: ~/.ctxloom/allowed_signers, the personal trust
// root `ctxloom signer trust` adds to. It is the same hazard one notch worse —
// a stray write there does not withhold content, it TRUSTS a signing key on
// the developer's machine, permanently — so it gets the same refusal.
//
// No override seam, unlike the approvals store: nothing needs one yet, and an
// injection point no caller uses is a branch no test can hold honest. The
// guard is the half that has to exist, because it protects callers that have
// not been written.
//
// The READ sites (ListSigners' user listing, config.Config.TrustRoot) are
// deliberately not routed through here. Their contract on an unresolvable home
// is to omit the user store silently, so a refusal would degrade rather than
// fail loud — the wrong shape for a guard. A read of the real trust root from
// a test is a lesser hazard than a write to it, and worth its own change.
func homeAllowedSignersPath() (string, error) {
	path, err := paths.HomeAllowedSignersPath()
	if err != nil {
		return "", err
	}
	if err := unsandboxedHomeError("user trust root", path,
		"testsupport.SandboxedMain / testsupport.Isolate, or --project against a temp checkout"); err != nil {
		return "", err
	}
	return path, nil
}

// unsandboxedHomeError is the belt to the override's braces: under a TEST
// BINARY, a home approvals store outside the OS temp root is refused rather
// than returned. In a production binary it is always nil — the real
// ~/.ctxloom/approvals is exactly where a real decision belongs.
//
// Why refuse instead of silently redirecting: a redirect would make the test
// pass while leaving the mistake in place, and the next home-rooted store
// added to this package would repeat it. Refusing names the fix at the moment
// the fix is cheap.
//
// The containment test mirrors testsupport.appDirIsolationError's HOME arm —
// "is this inside os.TempDir()" — deliberately as the same weak, stable fact
// rather than a prediction of which directory a harness will mint. Every
// sanctioned isolation in this repo satisfies it: testsupport.SandboxedMain
// and testsupport.Isolate root HOME at a temp dir, t.TempDir() is under the
// temp root, and tests/integration/testenv's os.MkdirTemp root is too.
func unsandboxedHomeError(what, dir, remedy string) error {
	if !runningUnderGoTest() {
		return nil
	}
	tempRoot := resolveRealPath(os.TempDir())
	if underTempRoot(dir, tempRoot) {
		return nil
	}
	return fmt.Errorf(
		"REFUSING to use the %s at %q from a test binary: it is outside the temp root %q, so anything recorded there "+
			"would land in the developer's real home and outlive this run. Isolate the test — %s",
		what, dir, tempRoot, remedy)
}

// runningUnderGoTest reports whether this process is a `go test` binary.
//
// It asks the flag set rather than importing "testing": testing.Init registers
// the test.* flags on flag.CommandLine before any test runs, and nothing else
// does, so the lookup is exact. Importing "testing" from shipped code would
// link the whole test harness — its regexp matcher, its profiler hooks — into
// the ctxloom binary, which is the cost internal/archlint's TestSupportAnalyzer
// exists to keep out.
func runningUnderGoTest() bool { return flag.Lookup("test.v") != nil }

// underTempRoot reports whether path is the temp root or lives beneath it.
func underTempRoot(path, tempRoot string) bool {
	path = resolveRealPath(path)
	if path == "" || tempRoot == "" {
		return false
	}
	if path == tempRoot {
		return true
	}
	return strings.HasPrefix(path, tempRoot+string(filepath.Separator))
}

// resolveRealPath returns p symlink-resolved and cleaned, falling back to the
// cleaned absolute form when it does not exist yet — an approvals directory is
// created lazily, so the path being asked about usually does not. The temp
// root is a symlink on macOS, so comparing unresolved paths would report a
// properly isolated store as unsandboxed.
func resolveRealPath(p string) string {
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real
	}
	// The leaf may not exist; resolve the deepest ancestor that does, so a
	// /tmp symlink still compares correctly against the resolved temp root.
	dir, leaf := filepath.Split(filepath.Clean(abs))
	if dir == "" || filepath.Clean(dir) == filepath.Clean(abs) {
		return filepath.Clean(abs)
	}
	return filepath.Join(resolveRealPath(filepath.Clean(dir)), leaf)
}

package operations

import (
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
	if err := paths.UnsandboxedHomeError("user countersignature store", dir,
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
	if err := paths.UnsandboxedHomeError("user trust root", path,
		"testsupport.SandboxedMain / testsupport.Isolate, or --project against a temp checkout"); err != nil {
		return "", err
	}
	return path, nil
}

// homeDistrustedSignersPath is the sibling chokepoint for the OTHER
// home-rooted store this package WRITES: ~/.ctxloom/distrusted_signers, the
// local-suppression record `ctxloom signer untrust` writes when the named
// principal matches ctxloom's own embedded key. It is the exact file an
// unguarded test run once wrote a permanent, machine-wide distrust of
// ctxloom's own publishing principal into — withholding every remote bundle
// from a freshly-initialised project with nothing telling the user why — so
// it gets the same refusal homeAllowedSignersPath gives its sibling store.
func homeDistrustedSignersPath() (string, error) {
	path, err := paths.HomeDistrustedSignersPath()
	if err != nil {
		return "", err
	}
	if err := paths.UnsandboxedHomeError("user distrusted-signers store", path,
		"testsupport.SandboxedMain / testsupport.Isolate, or --project against a temp checkout"); err != nil {
		return "", err
	}
	return path, nil
}

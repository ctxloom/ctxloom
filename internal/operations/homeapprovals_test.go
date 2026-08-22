package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// approvalsEntries lists the countersignature files under dir, treating a
// missing directory as "no records" — an approvals store is created lazily, so
// absent and empty are the same observation for these tests.
func approvalsEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestSetBlacklist_InjectedHomeRoot_WritesThereAndLeavesTheRealHomeUntouched is
// the decisive test for the defect: recording a home-scoped rejection wrote
// into the developer's REAL ~/.ctxloom/approvals, a durable decision that
// withheld content for every later test and outlived the run.
//
// It drives the FULL production write path — no injected UserStore, no injected
// filesystem, so countersign.Store writes through the real OS filesystem
// exactly as `ctxloom bundle reject` does — with only the home approvals ROOT
// redirected.
//
// It asserts BOTH sides, which is the whole point. The injected root alone is
// satisfied by an implementation that writes to both places; the assertion that
// nothing appeared under $HOME, and nothing under the process's genuine
// pre-sandbox home, is what proves the real location was never reached.
func TestSetBlacklist_InjectedHomeRoot_WritesThereAndLeavesTheRealHomeUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "") // no key: the degraded UNSIGNED path, as in the report

	sandboxHomeApprovals, err := paths.HomeApprovalsPath()
	require.NoError(t, err)
	// realHOME is captured by TestMain BEFORE any sandboxing, so this is the
	// developer's genuine ~/.ctxloom/approvals — the location the defect wrote
	// to. Snapshot it and require it unchanged; nothing here may create it.
	realHomeApprovals := filepath.Join(realHOME, paths.AppDirName, paths.ApprovalsDirName)
	realBefore := approvalsEntries(t, realHomeApprovals)

	injected := filepath.Join(t.TempDir(), "approvals")
	t.Cleanup(SetHomeApprovalsDirForTesting(injected))

	cfg, _ := realExposureProject(t, afero.NewMemMapFs())
	res, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked"})
	require.NoError(t, err)
	require.Equal(t, "user", res.Store, "the fixture must exercise the HOME-scoped store, not the project one")
	require.True(t, res.Unsigned, "the fixture must exercise the unsigned path the report describes")

	// Written THERE.
	written := approvalsEntries(t, injected)
	require.NotEmpty(t, written, "the rejection must be recorded in the injected root")
	var refRejects int
	for _, name := range written {
		if strings.HasSuffix(name, ".reject.unsigned") {
			refRejects++
		}
	}
	assert.NotZero(t, refRejects, "the injected root must hold the unsigned rejection records, got %v", written)

	// And NOWHERE ELSE.
	assert.Empty(t, approvalsEntries(t, sandboxHomeApprovals),
		"nothing may be written to the $HOME-resolved approvals store when a root is injected")
	assert.Equal(t, realBefore, approvalsEntries(t, realHomeApprovals),
		"the developer's real ~/.ctxloom/approvals must be byte-for-byte untouched by a test recording a rejection")
}

// TestBuildCountersignRecords_ReadsTheInjectedHomeRoot is the reader's half.
// The writer and the reader must resolve the SAME user store: a rejection
// recorded in one directory and looked for in another is a decision nothing
// honours, which is the silent no-op this codebase's trust plumbing has been
// bitten by before.
func TestBuildCountersignRecords_ReadsTheInjectedHomeRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	injected := filepath.Join(t.TempDir(), "approvals")
	t.Cleanup(SetHomeApprovalsDirForTesting(injected))

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "x"}
	refStr := mustCountersignRef(t, ref)
	require.NoError(t, countersign.NewStore(injected, afero.NewOsFs()).WriteUnsignedRefReject(refStr))

	records := buildCountersignRecords(nil, afero.NewOsFs(), nil, nil, nil)
	assert.True(t, records.user.HasUnsignedRefReject(refStr),
		"the reader must resolve the user store through the same seam the writer does")
}

// TestHomeApprovalsDir_RefusesAnUnsandboxedHomeUnderTest is the belt to the
// injection's braces, and the property the task actually asks for: with no
// override, a test binary must not be ABLE to reach the real home store by
// default. A seam only protects the tests that remember to use it.
func TestHomeApprovalsDir_RefusesAnUnsandboxedHomeUnderTest(t *testing.T) {
	// A real, non-temp home. Nothing here writes — resolution is refused
	// before any store is constructed.
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")

	dir, err := homeApprovalsDir()
	require.Error(t, err, "an unsandboxed HOME must be refused under a test binary, got %q", dir)
	assert.Empty(t, dir, "a refused resolution must not also hand back the path it refused")
	assert.Contains(t, err.Error(), "SetHomeApprovalsDirForTesting",
		"the refusal must name the fix, or it only tells the reader they are stuck")
}

// TestHomeApprovalsDir_AllowsASandboxedHome is the positive control: the guard
// above must not be firing for every test in the repo. Every sanctioned
// isolation (testsupport.SandboxedMain, testsupport.Isolate, t.TempDir) roots
// HOME under the temp root, and that must resolve normally.
func TestHomeApprovalsDir_AllowsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := homeApprovalsDir()
	require.NoError(t, err)
	assert.Equal(t, resolveRealPath(filepath.Join(home, paths.AppDirName, paths.ApprovalsDirName)), resolveRealPath(dir))
}

// TestUnsandboxedHomeError_IsInertOutsideATestBinary pins the half a test
// binary cannot observe about itself: in the shipped ctxloom the real
// ~/.ctxloom/approvals is exactly where a decision belongs, and the guard must
// never refuse it. Driven through the pure predicate with the test-binary
// answer forced, since runningUnderGoTest is true by construction here.
func TestUnsandboxedHomeError_IsInertOutsideATestBinary(t *testing.T) {
	const realHome = "/home/someone/.ctxloom/approvals"
	require.Error(t, unsandboxedHomeError(realHome), "precondition: this path is refused UNDER test")

	assert.True(t, runningUnderGoTest(), "the guard's trigger must be true in a test binary, or it never fires at all")
	assert.False(t, underTempRoot(realHome, resolveRealPath(os.TempDir())),
		"a real home approvals store is outside the temp root — the fact the guard turns on")
}

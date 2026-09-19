package operations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
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
	t.Cleanup(countersign.SetHomeDirForTesting(injected))

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
	t.Cleanup(countersign.SetHomeDirForTesting(injected))

	ref := trust.Ref{RepoURL: trustRepo, Bundle: "b", Kind: trust.KindFragment, Name: "x"}
	refStr := mustCountersignRef(t, ref)
	require.NoError(t, countersign.NewStore(injected, afero.NewOsFs()).WriteUnsignedRefReject(refStr))

	records := buildCountersignRecords(nil, afero.NewOsFs(), nil, nil, nil)
	assert.True(t, records.User().HasUnsignedRefReject(refStr),
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

	dir, err := countersign.HomeDir()
	require.Error(t, err, "an unsandboxed HOME must be refused under a test binary, got %q", dir)
	assert.Empty(t, dir, "a refused resolution must not also hand back the path it refused")
	assert.Contains(t, err.Error(), "SetHomeDirForTesting",
		"the refusal must name the fix, or it only tells the reader they are stuck")
}

// TestHomeApprovalsDir_AllowsASandboxedHome is the positive control: the guard
// above must not be firing for every test in the repo. Every sanctioned
// isolation (testsupport.SandboxedMain, testsupport.Isolate, t.TempDir) roots
// HOME under the temp root, and that must resolve normally.
func TestHomeApprovalsDir_AllowsASandboxedHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := countersign.HomeDir()
	require.NoError(t, err)
	assert.Equal(t, realPath(t, filepath.Join(home, paths.AppDirName, paths.ApprovalsDirName)), realPath(t, dir))
}

// TestHomeApprovalsDir_HonoursGOTMPDIREvenWhenOSTempDirDisagrees pins the
// mechanism this guard depends on, deterministically rather than relying on
// the ambient environment: go1.26.8's testing.(*common).makeTempDir builds
// t.TempDir() via os.MkdirTemp(os.Getenv("GOTMPDIR"), pattern) — reading
// GOTMPDIR directly and bypassing os.TempDir() (and the TMPDIR it honours)
// whenever GOTMPDIR is set. This project's justfile does exactly that
// (GOTMPDIR=/var/tmp/ctxloom-gotmp, kept off tmpfs /tmp to avoid ENOSPCing
// the linker under parallel builds), so a HOME sandboxed under GOTMPDIR must
// be accepted even though it disagrees with os.TempDir(). TMPDIR and GOTMPDIR
// are both pinned explicitly here so the test does not depend on whatever the
// ambient environment happens to export.
func TestHomeApprovalsDir_HonoursGOTMPDIREvenWhenOSTempDirDisagrees(t *testing.T) {
	scratch := t.TempDir()
	osRoot := filepath.Join(scratch, "os-temp-root")
	altRoot := filepath.Join(scratch, "gotmpdir-root")
	require.NoError(t, os.MkdirAll(osRoot, 0o700))
	require.NoError(t, os.MkdirAll(altRoot, 0o700))

	t.Setenv("TMPDIR", osRoot)
	require.Equal(t, realPath(t, osRoot), realPath(t, os.TempDir()),
		"precondition: TMPDIR must steer os.TempDir(), so this test controls both roots explicitly")

	t.Setenv("GOTMPDIR", altRoot)

	home := filepath.Join(altRoot, "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	t.Setenv("HOME", home)

	dir, err := countersign.HomeDir()
	require.NoError(t, err,
		"a HOME under the configured GOTMPDIR must be accepted even though it is outside os.TempDir() — "+
			"the exact live disagreement this project's justfile creates")
	assert.Equal(t, realPath(t, filepath.Join(home, paths.AppDirName, paths.ApprovalsDirName)), realPath(t, dir))
}

// TestHomeAllowedSignersPath_RefusesAnUnsandboxedHomeUnderTest covers the
// sibling home-rooted store this package writes. `ctxloom signer trust`
// resolves ~/.ctxloom/allowed_signers, so an unguarded test run would add a
// permanently trusted signing key to the developer's real trust root — the
// same defect as the approvals store, one notch worse.
func TestHomeAllowedSignersPath_RefusesAnUnsandboxedHomeUnderTest(t *testing.T) {
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")

	path, err := homeAllowedSignersPath()
	require.Error(t, err, "an unsandboxed HOME must be refused, got %q", path)
	assert.Empty(t, path)
	assert.Contains(t, err.Error(), "user trust root", "the refusal must name which store it is about")
}

// TestSignerStorePath_UserRefusesAnUnsandboxedHome proves the guard is wired
// into the single WRITE-destination resolver both `signer trust` (AddSigner)
// and `signer untrust` (RemoveSigner) now share, not only into the helper.
// An unwired guard is no guard.
func TestSignerStorePath_UserRefusesAnUnsandboxedHome(t *testing.T) {
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")

	path, _, _, err := signerStorePath(nil, false)
	require.Error(t, err, "the user allowed_signers destination must be refused, got %q", path)
}

// TestDistrustedSignersStorePath_UserRefusesAnUnsandboxedHome pins the same
// guard on the OTHER write-scoped store `signer untrust` resolves: the
// distrusted_signers file that, unguarded, once let a test/dev run
// permanently distrust ctxloom's own embedded publishing principal on a real
// machine (see RemoveSigner's embedded-suppression path).
func TestDistrustedSignersStorePath_UserRefusesAnUnsandboxedHome(t *testing.T) {
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")

	path, _, _, err := distrustedSignersStorePath(nil, false)
	require.Error(t, err, "the user distrusted_signers destination must be refused, got %q", path)
}

// TestSignerStorePath_ProjectIsUnaffected is the positive control: the guard
// governs the HOME store only. A project destination is inside the caller's
// own checkout and must resolve however the caller configured it.
func TestSignerStorePath_ProjectIsUnaffected(t *testing.T) {
	t.Setenv("HOME", string(filepath.Separator)+"ctxloom-unsandboxed-home")
	appDir := filepath.Join(t.TempDir(), paths.AppDirName)

	path, _, _, err := signerStorePath(gatedFixture(config.Fixture{AppPaths: []string{appDir}}), true)
	require.NoError(t, err)
	assert.Equal(t, paths.AllowedSignersPath(appDir), path)
}

// realPath normalizes a path for comparison the way the guard does: symlinks
// resolved, so a macOS/Linux temp root that IS a symlink does not read as a
// different directory from the path built under it.
func realPath(t *testing.T, p string) string {
	t.Helper()
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

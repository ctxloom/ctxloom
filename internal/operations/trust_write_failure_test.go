package operations

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// This file closes GAP3c (oval-stamp): trust.go's SetItemTrust/SetBlacklist
// each check the error a countersignature write returns
// (store.WriteApprove / store.WriteRefReject / the writeContentReject
// closure), but nothing in the suite ever made one of those writes actually
// FAIL — so inverting any of those checks (treating a failed persist as a
// success) left the whole suite green (44-survivor mutation run, ooze
// 2026-07-14). "No scenario notices, because every scenario only checks the
// downstream materialized payload — never that the decision was actually
// persisted." These tests inject a real write failure and assert the
// CALLER'S OWN REPORT of what got persisted is truthful, not merely that
// some exit code was zero.

// failWritesFs makes every WRITE-flagged OpenFile call fail — a fake (not a
// mock) simulating a disk gone bad (full, unwritable, permission revoked
// mid-session) without touching real OS permissions, the write-side sibling
// of trust_approvals_readable_test.go's read-side denyOpenFs fake. Reads
// (Open, used by Store.Readable/candidates/Verified) and directory creation
// (MkdirAll) are left alone: afero.WriteFile's failure mode is OpenFile
// itself returning an error, matching a real filesystem that can mkdir but
// then hits ENOSPC/EROFS writing the file body.
type failWritesFs struct {
	afero.Fs
}

func (failWritesFs) OpenFile(_ string, _ int, _ os.FileMode) (afero.File, error) {
	return nil, errors.New("simulated write failure: disk full")
}

// failAfterNWritesFs lets the first n write-flagged OpenFile calls through to
// the wrapped fs, then fails every one after that — simulating a disk that
// goes bad PARTWAY THROUGH a multi-write operation (SetBlacklist writes a
// ref-reject, then one content-reject per form the item currently has),
// rather than failWritesFs's uniform total failure. This is what proves a
// later failure does not retroactively corrupt an earlier successful write's
// record, and that the caller's report of "what I recorded" tracks reality
// form-by-form rather than all-or-nothing.
type failAfterNWritesFs struct {
	afero.Fs
	n     int
	count int
}

func (f *failAfterNWritesFs) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	f.count++
	if f.count > f.n {
		return nil, errors.New("simulated write failure: disk full")
	}
	return f.Fs.OpenFile(name, flag, perm)
}

// TestSetItemTrust_WriteFailureSurfacesError proves a failed WriteApprove is
// not silently swallowed into a reported "approved": SetItemTrust must
// return a non-nil error and no result, and the countersignature store must
// hold nothing afterward — not even a partial/corrupt trace of the attempt.
func TestSetItemTrust_WriteFailureSurfacesError(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	mem := afero.NewMemMapFs()
	failingStore := countersign.NewStore("/user-approvals", failWritesFs{Fs: mem})

	res, err := SetItemTrust(nil, SetItemTrustRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/solid", Signer: fx.signer, Root: fx.root, UserStore: failingStore, Loader: loader,
	})
	require.Error(t, err, "a failed countersignature write must surface as an error, not a silently reported approval")
	assert.Nil(t, res)

	entries, derr := afero.ReadDir(mem, "/user-approvals")
	require.NoError(t, derr)
	assert.Empty(t, entries, "a failed write must leave no countersignature file behind, not a partial one")

	// The item must still resolve pending against the REAL fixture store —
	// nothing was recorded anywhere a caller could later trust as approved.
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	got, everr := EffectiveTrust(nil, EffectiveTrustRequest{Ref: tref, Payload: []byte("always raw fragment body"), Form: rawForm, Records: fx.records()})
	require.NoError(t, everr)
	assert.Equal(t, trust.Deny, got.Decision)
	assert.Equal(t, trust.SourcePending, got.Source)
}

// TestSetBlacklist_RefRejectWriteFailureSurfacesError proves the ref-level
// (sticky) block's write failure is not silently swallowed either: it is the
// FIRST write SetBlacklist attempts and the durable half of a rejection
// (spec §5.3) — a failure there must abort the whole call with an error, not
// report "rejected" while nothing was recorded.
func TestSetBlacklist_RefRejectWriteFailureSurfacesError(t *testing.T) {
	loader, _ := seededLoader(t)
	fx := newTrustFixture(t)
	mem := afero.NewMemMapFs()
	failingStore := countersign.NewStore("/user-approvals", failWritesFs{Fs: mem})

	res, err := SetBlacklist(nil, SetBlacklistRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/solid", Signer: fx.signer, Root: fx.root, UserStore: failingStore, Loader: loader,
	})
	require.Error(t, err, "a failed ref-reject countersignature write must surface as an error, not a silently reported rejection")
	assert.Nil(t, res)

	entries, derr := afero.ReadDir(mem, "/user-approvals")
	require.NoError(t, derr)
	assert.Empty(t, entries, "a failed ref-reject write must leave no countersignature file behind")
}

// TestSetBlacklist_ContentRejectWriteFailure_NotFalselyReported closes the
// sharpest half of GAP3c: writeContentReject's per-form write is
// deliberately best-effort by DESIGN (a rejection must still succeed when
// the item's content is already gone), so a write failure there does not —
// and should not — abort the whole call once the durable ref-level block
// already landed. But "best-effort" must never mean "report success anyway":
// SetBlacklistResult.ContentForms is the caller's own account of what it
// persisted, and nothing in the suite ever made one of the two content-reject
// writes fail while the other succeeded, so a caller reporting the FAILED
// form as recorded (or omitting the form that actually succeeded) left the
// whole suite green. This drives a dual-form item's rejection through a disk
// that goes bad after the ref-reject and the raw content-reject already
// landed, and proves the report — and the store itself — agree: raw is
// recorded, distilled is not.
func TestSetBlacklist_ContentRejectWriteFailure_NotFalselyReported(t *testing.T) {
	loader, _ := seededLoader(t) // "tooling#fragments/dual" ships raw+distilled
	fx := newTrustFixture(t)
	mem := afero.NewMemMapFs()
	// Call order inside SetBlacklist: 1) ref-reject, 2) raw content-reject,
	// 3) distilled content-reject. Let the first two through, fail the third.
	failing := &failAfterNWritesFs{Fs: mem, n: 2}
	store := countersign.NewStore("/user-approvals", failing)

	res, err := SetBlacklist(nil, SetBlacklistRequest{
		Ref: "https://github.com/acme/repo@bundles/tooling#fragments/dual", Signer: fx.signer, Root: fx.root, UserStore: store, Loader: loader,
	})
	require.NoError(t, err, "the durable ref-level block succeeded, so the call as a whole must still report success")
	require.NotNil(t, res)
	assert.Equal(t, []string{"raw"}, res.ContentForms,
		"the distilled content-reject write failed and must NOT be reported as recorded — inverting the error check would report it anyway")

	// Prove it from the store itself, not just the report.
	_, rawOK := store.VerifiedContentReject(signing.AttestFragmentRaw, []byte("raw body"), fx.root, time.Now())
	assert.True(t, rawOK, "the raw content-reject that succeeded must actually verify from the store")
	_, distilledOK := store.VerifiedContentReject(signing.AttestFragmentDistilled, []byte("distilled body"), fx.root, time.Now())
	assert.False(t, distilledOK, "the distilled content-reject write failed and must not verify as recorded")

	// The sticky ref-level block itself is unaffected by the later content
	// write failure — it already landed on write #1.
	_, refOK := store.VerifiedRefReject(countersignRef(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "dual"}), fx.root, time.Now())
	assert.True(t, refOK, "the ref-level block succeeded on write #1 and must not be undone by a later, unrelated write failure")
}

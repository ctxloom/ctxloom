package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The third verb: returning a decided item to undecided.
//
// Every case here asserts the EFFECT — delivered, withheld, or back to pending
// — through EffectiveTrust, the same decision function assembly runs, and
// never the mutation's own report of itself. A `forget` that removed nothing
// would still return a tidy result naming the ref it was handed.
//
// The negative assertions are proven capable of failing before they are
// trusted: each case establishes the item in the OPPOSITE state first, in the
// same fixture, so "no longer rejected" is read off a fixture that was
// demonstrably rejected a line earlier rather than off one where the rejection
// never landed.

// solidRawBody is the fragment payload the review fixture's untrusted remote
// bundle exposes — the exact bytes a decision about "solid" binds to.
const solidRawBody = "solid raw body"

// forgetSolidRef is that fragment's ref: REMOTE and unsigned, so it starts pending
// and stays pending unless a decision says otherwise. A project-local item
// would read "accepted" from the first-party exemption before anyone decided
// anything, which makes every approval assertion tautological.
func forgetSolidRef() trust.Ref {
	return trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindFragment, Name: "solid"}
}

// solidState resolves the fragment's effective review state through the
// production decision function over fx's stores.
func solidState(t *testing.T, fx *trustFixture) trust.State {
	t.Helper()
	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref:     forgetSolidRef(),
		Payload: []byte(solidRawBody),
		Form:    rawForm,
		Records: fx.records(),
	})
	require.NoError(t, err)
	return res.State()
}

// TestForgetItemDecision_ClearsARejection is the case the verb exists for.
// `trust` and `reject` alone can move an item between the two DECIDED states
// forever without ever restoring the third, so a rejection recorded in error
// could only be answered with an approval — and an approval is not a
// withdrawal. This proves the rejection is gone, not overridden: the item
// reads PENDING afterwards, not accepted.
func TestForgetItemDecision_ClearsARejection(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	ref := reviewSeedKey + "#fragments/solid"

	// The fixture must be able to reach BOTH decided states, or the assertions
	// below prove nothing about which one is being cleared.
	require.Equal(t, trust.StatePending, solidState(t, fx), "an unreviewed remote item starts pending")
	_, err := SetBlacklist(nil, SetBlacklistRequest{Ref: ref, UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	require.Equal(t, trust.StateRejected, solidState(t, fx), "the rejection must be in force before it can meaningfully be cleared")

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{Ref: ref, UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	assert.Contains(t, res.Cleared, "rejection", "the result must name the rejection it removed")
	assert.Positive(t, res.Records)

	assert.Equal(t, trust.StatePending, solidState(t, fx),
		"forgetting a rejection returns the item to PENDING — undecided, awaiting review. "+
			"Anything else means the rejection was overridden rather than withdrawn")
	assert.Empty(t, res.StillDecided)
}

// TestForgetItemDecision_ClearsARejectionInBothComponents. A rejection is two
// records: the sticky ref block and a ref-omitted content block that follows
// the bytes wherever they are republished. Clearing only the first leaves the
// same content rejected under every other name, which is a rejection still in
// force reported as cleared.
func TestForgetItemDecision_ClearsARejectionInBothComponents(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	ref := reviewSeedKey + "#fragments/solid"

	_, err := SetBlacklist(nil, SetBlacklistRequest{Ref: ref, UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)

	// The content block is ref-omitted, so it denies these bytes under a
	// COMPLETELY DIFFERENT ref. That is the component this asserts on.
	elsewhere := trust.Ref{RepoURL: trustRepo, Bundle: "other", Kind: trust.KindFragment, Name: "copy"}
	rejectedElsewhere := func() bool { return fx.records().Rejected(elsewhere, []byte(solidRawBody)) }
	require.True(t, rejectedElsewhere(), "a moved copy of the rejected bytes must be rejected too, or there is no content block to clear")

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{Ref: ref, UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	assert.Contains(t, res.ContentForms, string(signing.FormRaw))
	assert.False(t, rejectedElsewhere(), "the ref-omitted content block must go with the ref block; a half-cleared rejection is still a rejection")
}

// TestForgetItemDecision_ClearsAnApproval: the same verb, the other decided
// state. The item stops being delivered and becomes pending again — it is NOT
// rejected, which is precisely the distinction `untrust` could not express.
func TestForgetItemDecision_ClearsAnApproval(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	ref := reviewSeedKey + "#fragments/solid"

	_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	require.Equal(t, trust.StateAccepted, solidState(t, fx), "the approval must deliver the item before it can meaningfully be cleared")

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{Ref: ref, UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	assert.Contains(t, res.Cleared, "approval")

	assert.Equal(t, trust.StatePending, solidState(t, fx),
		"forgetting an approval withdraws it — the item awaits review again, and is NOT rejected")
}

// TestForgetItemDecision_ReturnsTheItemToTheReviewQueue. "As if it had never
// been reviewed" is a claim about the pending walk, not only about the gate: a
// cleared item must be offered again, and offered as NEW rather than as an
// update to a decision that no longer exists.
func TestForgetItemDecision_ReturnsTheItemToTheReviewQueue(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	ref := reviewSeedKey + "#fragments/solid"

	_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: ref, UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	pending, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: loader, FS: fs})
	require.NoError(t, err)
	require.NotContains(t, pendingRefs(pending), ref, "an approved item is not pending")

	_, err = ForgetItemDecision(nil, ForgetItemDecisionRequest{Ref: ref, UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)

	pending, err = PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: loader, FS: fs})
	require.NoError(t, err)
	assert.Equal(t, ReviewStatusNew, pendingRefs(pending)[ref],
		"a cleared item comes back as NEW: there is no earlier decision left for it to be an update of")
}

// TestForgetItemDecision_LeavesEveryOtherDecisionStanding. A forget that swept
// the store would satisfy every assertion above while destroying the rest of a
// user's review history.
func TestForgetItemDecision_LeavesEveryOtherDecisionStanding(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()

	_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)
	_, err = SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#commands/greet", UserStore: fx.user, Signer: fx.signer, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)

	_, err = ForgetItemDecision(nil, ForgetItemDecisionRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs})
	require.NoError(t, err)

	greet := trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindPrompt, Name: "greet"}
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{Ref: greet, Payload: []byte("greet body"), Form: rawForm, Records: fx.records()})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, got.Decision, "the untouched neighbour's approval must survive")
}

// TestForgetItemDecision_NeedsNoSigningKey. Forgetting writes nothing, so it
// asserts nothing anyone else must honour, so it needs neither a key nor a
// namespace grant. Requiring one would make an UNSIGNED decision — recorded by
// exactly the user who has no key — the one decision that could never be
// withdrawn.
func TestForgetItemDecision_NeedsNoSigningKey(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	refStr := countersignRef(forgetSolidRef())

	// The degraded unsigned path, recorded with no key at all.
	require.NoError(t, fx.user.WriteUnsignedRefReject(refStr))
	require.Equal(t, trust.StateRejected, solidState(t, fx))

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Root: fx.root, Loader: loader, FS: fs,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Cleared, "rejection")
	assert.Equal(t, trust.StatePending, solidState(t, fx))
}

// TestForgetItemDecision_UnresolvableItem_StillClearsTheStickyBlock. A
// rejection outlives the content it was made about — that is what makes it
// sticky — so withdrawing one must work when the item is gone from the bundle.
// Only the content component needs the bytes, and its absence is REPORTED
// rather than rounded up into a clean sweep.
func TestForgetItemDecision_UnresolvableItem_StillClearsTheStickyBlock(t *testing.T) {
	fx := newTrustFixture(t)
	fs := afero.NewMemMapFs()
	ghost := trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindFragment, Name: "ghost"}
	require.NoError(t, fx.user.WriteRefReject(countersignRef(ghost), fx.signer))

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: reviewSeedKey + "#fragments/ghost", UserStore: fx.user, Root: fx.root,
		Loader: reviewLoader(t, reviewBundle()), FS: fs,
	})
	require.NoError(t, err)
	assert.Contains(t, res.Cleared, "rejection")
	assert.False(t, res.ContentResolved, "the caller must be told the content could not be reached, not left to assume it was")
	assert.False(t, fx.records().Rejected(ghost, nil), "the sticky block must be gone")
}

// TestForgetItemDecision_NothingRecorded_IsReportedAsSuch. Forgetting an
// undecided item is a no-op, and saying "cleared" over it would teach a user
// that the command works when they have in fact addressed the wrong ref.
func TestForgetItemDecision_NothingRecorded_IsReportedAsSuch(t *testing.T) {
	fx := newTrustFixture(t)
	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Root: fx.root,
		Loader: reviewLoader(t, reviewBundle()), FS: afero.NewMemMapFs(),
	})
	require.NoError(t, err)
	assert.Empty(t, res.Cleared)
	assert.Zero(t, res.Records)
	assert.Equal(t, ForgetStatusNothingRecorded, res.Status)
	assert.False(t, res.ClearedAnything(), "the predicate every renderer branches on must agree with the status")
}

// TestForgetItemDecision_SaysSoWhenTheOtherStoreStillDecides. Decisions live in
// two stores and this clears ONE of them. An inherited project rejection that
// survives a personal `forget` leaves the item exactly as withheld as before —
// the silent no-op, with a success message over it — so the outcome is
// reported instead.
func TestForgetItemDecision_SaysSoWhenTheOtherStoreStillDecides(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	refStr := countersignRef(forgetSolidRef())

	// The team's committable rejection, plus a personal one on top of it.
	require.NoError(t, fx.project.WriteRefReject(refStr, fx.signer))
	require.NoError(t, fx.user.WriteRefReject(refStr, fx.signer))

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, ProjectStore: fx.project,
		Root: fx.root, Loader: loader, FS: fs,
	})
	require.NoError(t, err)
	assert.Equal(t, "rejected", res.StillDecided,
		"the personal record went, the project one did not, and the item is still withheld — say so")
	assert.Equal(t, trust.StateRejected, solidState(t, fx))
}

// TestForgetItemDecision_ProjectStore targets the committable store, so a team
// lead can withdraw a shared decision the same way.
func TestForgetItemDecision_ProjectStore(t *testing.T) {
	fx := newTrustFixture(t)
	loader := reviewLoader(t, reviewBundle())
	fs := afero.NewMemMapFs()
	require.NoError(t, fx.project.WriteRefReject(countersignRef(forgetSolidRef()), fx.signer))
	require.Equal(t, trust.StateRejected, solidState(t, fx))

	res, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: reviewSeedKey + "#fragments/solid", Project: true,
		UserStore: fx.user, ProjectStore: fx.project, Root: fx.root, Loader: loader, FS: fs,
	})
	require.NoError(t, err)
	assert.Equal(t, "project", res.Store)
	assert.Equal(t, trust.StatePending, solidState(t, fx))
}

// TestForgetItemDecision_MalformedRef_Refuses: a ref that names no item cannot
// have a decision cleared, and answering "nothing was recorded" would hide a
// typo behind a plausible outcome.
func TestForgetItemDecision_MalformedRef_Refuses(t *testing.T) {
	fx := newTrustFixture(t)
	_, err := ForgetItemDecision(nil, ForgetItemDecisionRequest{
		Ref: "not-a-ref", UserStore: fx.user, Root: fx.root, FS: afero.NewMemMapFs(),
	})
	require.Error(t, err)
}

package operations

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The trust-keying property outdated-recoil exists to establish: an item's
// trust identity comes from WHERE its bundle was found, never from the `name:`
// the bundle DECLARES about itself.
//
// The declared name is CONTENT — covered by the publish signature and by
// review — so it cannot also be an input to the decision that establishes that
// trust. Before the fix, Bundle.contentSourceRef fell back to Bundle.Name when
// sourceRef was empty, which is the case for every project (fs) bundle; and
// since Bundle.Name became a declared YAML field, that fallback let a bundle
// choose the key its own decisions are recorded under.
//
// Every test below drives the REAL readers (a project bundle is real YAML with
// a real `name:` in it, read by NewProjectReader) and the REAL gate
// (contentGate over signed countersignature stores), because the whole bug
// lived in the string one production function derived from a parsed document.
// A fixture that hand-built the trust ref could not have caught it and cannot
// guard it.

const impostorRemoteRef = "https://example.test/repo@bundles/kit"

// declaredNameGate builds the production content gate over a fresh signed
// countersignature fixture, plus the fixture itself so a test can record
// decisions into it.
func declaredNameGate(t *testing.T) (*contentGate, *trustFixture) {
	t.Helper()
	fx := newTrustFixture(t)
	return &contentGate{cfg: config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}}), records: fx.records()}, fx
}

// TEST 1 — A GRANT DOES NOT TRANSFER TO A BUNDLE THAT CLAIMS ITS NAME.
//
// A decision is recorded against ONE identity and must attach to that identity
// only. A project bundle that declares `name:` equal to a remote bundle's
// canonical ref is a different bundle, in a different place, shipping
// different bytes; nothing recorded about the remote may follow it, and
// nothing about it may follow the remote.
//
// The probe is a REF-SCOPED rejection of the remote item, because a ref-scoped
// decision is the one whose reach is decided entirely by the trust ref — which
// is the string this fix changes. (A content-scoped decision is deliberately
// ref-agnostic; see TEST 2.) Rejection is also step 1 of the cascade, ahead of
// every allow including the first-party exemption, so it is observable on
// project content — which the approve/allow steps are not, since project
// content is admitted by POSTURE at step 3 whatever its ref says.
//
// Before the fix the impostor's trust ref WAS the remote's canonical ref, so
// the two items shared one identity: rejecting the remote silently withheld an
// unrelated project bundle's content, and — the direction that matters —
// anything recorded for either was indistinguishable from a record about the
// other. Two bundles keyed alike is the conflation; which way the first
// decision happens to point is an accident of the test, not of the defect.
func TestDeclaredName_RemoteDecisionDoesNotTransferToProjectBundleClaimingItsRef(t *testing.T) {
	gate, fx := declaredNameGate(t)

	// A remote (pinned, repofs-read) bundle, and a PROJECT bundle whose YAML
	// declares that remote's canonical ref as its own name while shipping
	// different bytes under the same item name. The map key is the LOCATION:
	// "impostor" becomes /bundles/impostor.yaml under NewProjectReader.
	loader := seedLoader(t, map[string]*bundles.Bundle{
		impostorRemoteRef: {Version: "1.0", Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: "REMOTE-BODY"},
		}},
		"impostor": {Name: impostorRemoteRef, Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: "PROJECT-BODY"},
		}},
	})
	pipe := bundles.NewPipeline(loader, gate, true)

	// The declared name really is in the document the reader parsed — if the
	// impostor did not actually claim the remote's ref there is no attack here
	// and every assertion below would pass vacuously.
	impostorRead := readOf(t, loader, "impostor")
	require.Equal(t, impostorRemoteRef, impostorRead.Bundle.Name,
		"fixture: the project bundle must actually DECLARE the remote's canonical ref")
	require.Equal(t, bundles.ProvenanceProject, impostorRead.Provenance,
		"fixture: it must still be read as project content — the declared name may not change WHERE it was found")

	// Sanity, and the guard against a vacuous pass: BOTH items are delivered
	// before any decision is recorded. Without this, "withheld" below could
	// mean "was never reachable".
	approveRemoteKeeper(t, fx, "REMOTE-BODY")
	remoteBefore, err := pipe.GetFragment(impostorRemoteRef + "#fragments/keeper")
	require.NoError(t, err, "the approved remote fragment must deliver before any rejection")
	require.Equal(t, "REMOTE-BODY", remoteBefore.Content)

	impostorBefore, err := pipe.GetFragment("impostor#fragments/keeper")
	require.NoError(t, err, "the project fragment must deliver before any rejection")
	require.Equal(t, "PROJECT-BODY", impostorBefore.Content)

	// Reject the REMOTE identity, ref-scoped, exactly as a human reviewing that
	// remote item would.
	remoteTRef, _, _, err := trust.ParseItemRef(impostorRemoteRef + "#fragments/keeper")
	require.NoError(t, err)
	fx.rejectRef(remoteTRef)

	// The rejection bites the identity it names.
	_, err = pipe.GetFragment(impostorRemoteRef + "#fragments/keeper")
	require.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"sanity: the rejected remote item must be withheld, got %v", err)

	// THE LOAD-BEARING ASSERTION. The project bundle merely CLAIMED that name.
	// Its content lives somewhere else, and no decision has ever been recorded
	// about where it lives, so the remote's rejection must not reach it.
	got, err := pipe.GetFragment("impostor#fragments/keeper")
	require.NoError(t, err,
		"a decision recorded for the REMOTE ref must not attach to a project bundle that only DECLARED that name — "+
			"two bundles sharing one trust identity is the conflation outdated-recoil closes; got %v", err)
	assert.Equal(t, "PROJECT-BODY", got.Content,
		"and it must be the project bundle's own bytes that survive")
}

// The identity separation TEST 1 proves through the gate, stated directly on
// the trust ref the loader derives — so a reader can see WHICH string moved,
// not only that a verdict changed.
//
// Asserted as a pair. "The impostor keys local" alone would still pass if the
// remote had also collapsed to something local; "the remote keys remote" alone
// would pass if the impostor had never claimed anything. Both together say the
// two are separate, and say which is which.
func TestDeclaredName_ProjectBundleKeysByLocationNotByDeclaredName(t *testing.T) {
	loader := seedLoader(t, map[string]*bundles.Bundle{
		impostorRemoteRef: {Version: "1.0", Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: "REMOTE-BODY"},
		}},
		"impostor": {Name: impostorRemoteRef, Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: "PROJECT-BODY"},
		}},
	})

	impostorItems, err := loader.ReadFragment("impostor#fragments/keeper")
	require.NoError(t, err)
	require.Len(t, impostorItems, 1)
	assert.Equal(t, "impostor#fragments/keeper", impostorItems[0].TrustRef,
		"a project bundle's trust ref is its LOCATION-derived resolution name, never the name it declared")

	impostorRef, _, _, err := trust.ParseItemRef(impostorItems[0].TrustRef)
	require.NoError(t, err)
	assert.True(t, impostorRef.IsLocal, "the impostor must key as local project content")
	assert.Empty(t, impostorRef.RepoURL,
		"and must NOT pick up the remote repository it named — that URL is what a grant is keyed under")

	remoteItems, err := loader.ReadFragment(impostorRemoteRef + "#fragments/keeper")
	require.NoError(t, err)
	require.Len(t, remoteItems, 1)
	remoteRef, _, _, err := trust.ParseItemRef(remoteItems[0].TrustRef)
	require.NoError(t, err)
	assert.False(t, remoteRef.IsLocal, "the real remote item must still key as remote")

	assert.NotEqual(t, remoteRef.Key(), impostorRef.Key(),
		"the two must occupy DIFFERENT trust store keys — one key for two bundles is the defect")
}

// TEST 2 — A RENAME DOES NOT LAUNDER REJECTED BYTES.
//
// signing.ContentRejectCountersignPayload omits the ref by design (spec §5.3):
// a content rejection follows identical BYTES wherever they appear, so moving
// or renaming a copy cannot revive them. This test pins that property against
// the specific evasion this fix's subject matter suggests — declare a different
// `name:` and ship the rejected bytes anyway.
//
// HONESTY NOTE, load-bearing for anyone reading the mutation log: this test is
// a REGRESSION GUARD, not a proof of the outdated-recoil fix. It passes both
// before and after that fix and no mutation of it can die, because the property
// it asserts is ref-agnostic by construction — which is exactly the property
// being pinned. It is not a tautology either: it dies to mutations of the
// CONTENT-REJECT path. Measured — replacing the `if len(payload) == 0` guard in
// countersignRecords.Rejected with a bare `return false`, so only the ref-level
// component survives, fails THIS test at the withhold assertion and leaves the
// other three in this file passing. That is the only thing it is entitled to
// claim.
func TestDeclaredName_ContentRejectionSurvivesADeclaredRename(t *testing.T) {
	const rejectedBody = "REJECTED-BYTES"
	gate, fx := declaredNameGate(t)

	// The bytes are rejected by CONTENT, with no ref component at all.
	fx.rejectContent(trust.KindFragment, signing.FormRaw, []byte(rejectedBody))

	// A project bundle declaring a name unrelated to anything the rejection
	// mentioned, shipping those exact bytes.
	loader := seedLoader(t, map[string]*bundles.Bundle{
		"launderer": {Name: "a-completely-different-name", Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: rejectedBody},
		}},
	})
	pipe := bundles.NewPipeline(loader, gate, true)

	_, err := pipe.GetFragment("launderer#fragments/keeper")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"content-rejected bytes must stay withheld however the bundle carrying them is named, got %v", err)

	// Not a vacuous pass: the SAME bundle, same name, same location, differing
	// only in its bytes, is delivered. So the withhold above is about the
	// bytes — it is not this fixture being unreachable or this name being
	// blocked.
	cleanLoader := seedLoader(t, map[string]*bundles.Bundle{
		"launderer": {Name: "a-completely-different-name", Fragments: map[string]bundles.BundleFragment{
			"keeper": {Content: "DIFFERENT-BYTES"},
		}},
	})
	clean, err := bundles.NewPipeline(cleanLoader, gate, true).GetFragment("launderer#fragments/keeper")
	require.NoError(t, err, "control: un-rejected bytes under the same name and location must deliver")
	require.Equal(t, "DIFFERENT-BYTES", clean.Content)
}

// TEST 3 — SELF-RENAME IS FAIL-CLOSED.
//
// The sharpest form of the defect, and a genuine widening rather than a
// conflation: a project bundle whose item has been rejected edits its own
// `name:` and thereby moves off the ref that rejection was recorded against.
// The bytes it delivers are unchanged; only the string it says about itself
// changed, and that is enough to walk out from under a human's decision.
//
// Ref-scoped, deliberately. A content-scoped rejection would withhold these
// bytes wherever they went (TEST 2) and would therefore mask this entirely.
//
// Post-fix the trust ref is the bundle's LOCATION, which the document cannot
// edit, so the rename is inert and the rejection holds.
func TestDeclaredName_SelfRenameDoesNotEscapeAnExistingRejection(t *testing.T) {
	const body = "SELF-RENAME-BODY"
	gate, fx := declaredNameGate(t)

	// The bundle as first reviewed: at /bundles/mine.yaml, declaring nothing.
	original := seedLoader(t, map[string]*bundles.Bundle{
		"mine": {Fragments: map[string]bundles.BundleFragment{"keeper": {Content: body}}},
	})
	originalPipe := bundles.NewPipeline(original, gate, true)

	// Delivered before the rejection — the guard against a vacuous withhold.
	before, err := originalPipe.GetFragment("mine#fragments/keeper")
	require.NoError(t, err, "the item must deliver before it is rejected")
	require.Equal(t, body, before.Content)

	// A human rejects it, ref-scoped, at the identity the loader actually
	// derived for it — not at a ref this test spelled out by hand.
	items, err := original.ReadFragment("mine#fragments/keeper")
	require.NoError(t, err)
	require.Len(t, items, 1)
	tRef, _, _, err := trust.ParseItemRef(items[0].TrustRef)
	require.NoError(t, err)
	fx.rejectRef(tRef)

	_, err = originalPipe.GetFragment("mine#fragments/keeper")
	require.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"sanity: the rejection must take effect at all, got %v", err)

	// THE ATTACK: same file, same location, same bytes — the document now
	// DECLARES a name. Nothing a human reviewed has changed.
	renamed := seedLoader(t, map[string]*bundles.Bundle{
		"mine": {Name: "freshly-renamed", Fragments: map[string]bundles.BundleFragment{"keeper": {Content: body}}},
	})
	renamedRead := readOf(t, renamed, "mine")
	require.Equal(t, "freshly-renamed", renamedRead.Bundle.Name,
		"fixture: the rename must actually be in the parsed document")

	_, err = bundles.NewPipeline(renamed, gate, true).GetFragment("mine#fragments/keeper")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a bundle must not escape a rejection by editing its own `name:` — the declared name is CONTENT, "+
			"and content may not choose the key its own decisions are recorded under; got %v", err)
}

// approveRemoteKeeper records a real signed approval for the remote fixture's
// keeper fragment at the ref the loader derives for it.
func approveRemoteKeeper(t *testing.T, fx *trustFixture, body string) {
	t.Helper()
	tRef, _, _, err := trust.ParseItemRef(impostorRemoteRef + "#fragments/keeper")
	require.NoError(t, err)
	fx.approve(tRef, signing.FormRaw, []byte(body))
}

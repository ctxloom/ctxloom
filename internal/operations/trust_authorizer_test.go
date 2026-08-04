package operations

import (
	"bytes"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The rows the decision table keys on the READ for, decided by the production
// Authorizer (contentGate.Admit) rather than by a spelled-out fixture.
//
// These are the rules phase A left in bundles.Loader.admit with a comment naming
// this slice as their removal point. The loader could only DROP: a bundle it
// withheld was unaddressable everywhere, with no reason a user could act on and
// no way for a review surface to see the same fact. Here they are verdicts.

const authorizerRemoteRef = "https://example.test/repo@bundles/kit"

// authorizerBundle is the fixture content every test below decides about: one
// fragment, so the exposure carries real bytes.
func authorizerBundle() *bundles.Bundle {
	return &bundles.Bundle{Version: "1.0", Fragments: map[string]bundles.BundleFragment{"keeper": {Content: "KEEPER-PAYLOAD"}}}
}

// admitFragment runs one fragment through the authorizer with the given read and
// returns the verdict.
func admitFragment(t *testing.T, g *contentGate, read bundles.BundleRead, ref string, body string) bundles.Verdict {
	t.Helper()
	return bundles.Decide(g, read, ref, []byte(body), bundles.FormRaw)
}

// --- remote | invalid: TAMPER, never degraded to unsigned ------------------

// A trusted key's signature that does not cover these bytes is TAMPER. It must
// be withheld and it must be REPORTED as tampered — degrading it to
// unsigned/pending is the spec §10.2 downgrade: an attacker corrupts a `.sig`,
// signed content becomes merely reviewable, and a human approves it.
func TestAuthorizer_RemoteInvalidSignatureIsTampered_NotDegradedToUnsigned(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}
	read := readOf(t, seedTampered(t, authorizerRemoteRef, "publisher@example.test", authorizerBundle()), authorizerRemoteRef)

	v := admitFragment(t, g, read, authorizerRemoteRef+"#fragments/keeper", "KEEPER-PAYLOAD")

	assert.False(t, v.Admit, "tampered remote content must be withheld")
	assert.Equal(t, bundles.ReasonTampered, v.Reason,
		"and reported as TAMPERED — 'unsigned' is the downgrade this row exists to close")
	assert.NotEmpty(t, v.Detail, "the signature failure has to be nameable")
	assert.NotEqual(t, bundles.ReasonUnsigned, v.Reason)
}

// The same bytes with a human's APPROVAL on them are still tampered. An
// approval covers bytes, not a signature, so it cannot un-tamper anything —
// and this is the arm that actually closes §10.2, because the downgrade's whole
// payoff is getting a human to approve content that was signed.
func TestAuthorizer_RemoteInvalidSignatureBeatsAnApproval(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	read := readOf(t, seedTampered(t, authorizerRemoteRef, "publisher@example.test", authorizerBundle()), authorizerRemoteRef)
	itemRef := authorizerRemoteRef + "#fragments/keeper"
	tRef, _, _, err := trust.ParseItemRef(itemRef)
	require.NoError(t, err)
	fx.approve(tRef, signing.FormRaw, []byte("KEEPER-PAYLOAD"))

	g := &contentGate{cfg: cfg, records: fx.records()}
	v := admitFragment(t, g, read, itemRef, "KEEPER-PAYLOAD")

	assert.False(t, v.Admit, "an approval does not un-tamper a signature that no longer covers the bytes")
	assert.Equal(t, bundles.ReasonTampered, v.Reason)
}

// Rejection is STEP 1 and stays above the tamper rule: both withhold, and the
// user is told the more specific, actionable thing — their own decision.
func TestAuthorizer_RejectionOutranksTamper(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	read := readOf(t, seedTampered(t, authorizerRemoteRef, "publisher@example.test", authorizerBundle()), authorizerRemoteRef)
	itemRef := authorizerRemoteRef + "#fragments/keeper"
	tRef, _, _, err := trust.ParseItemRef(itemRef)
	require.NoError(t, err)
	fx.rejectRef(tRef)

	g := &contentGate{cfg: cfg, records: fx.records()}
	v := admitFragment(t, g, read, itemRef, "KEEPER-PAYLOAD")

	assert.False(t, v.Admit)
	assert.Equal(t, bundles.ReasonRejected, v.Reason,
		"rejection is step 1; a user who rejected this must be told THAT, not a signature diagnosis")
}

// --- rejection reaches every exemption -------------------------------------

// Rejection beats the first-party exemptions — all three of them. A user who
// rejected a builtin keeps that rejection, which is the property that made
// builtin a distinct STEP below rejection rather than a gate bypass.
func TestAuthorizer_RejectionReachesEveryFirstPartyExemption(t *testing.T) {
	cases := []struct {
		name string
		ref  trust.Ref
		read bundles.BundleRead
	}{
		{"local", trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsLocal: true},
			bundles.ProjectAuthoredRead("kit", authorizerBundle())},
		{"builtin", trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsBuiltin: true}, builtinLikeRead(t)},
		{"companion", trust.Ref{RepoURL: "ctxloom:companion", Bundle: "ltk", Kind: trust.KindFragment, Name: "keeper", IsCompanion: true},
			companionLikeRead(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
			fx := newTrustFixture(t)

			g := &contentGate{cfg: cfg, records: fx.records()}
			allowed := g.Admit(bundles.Exposure{Read: tc.read, Ref: tc.ref, Bytes: []byte("KEEPER-PAYLOAD"), Form: bundles.FormRaw})
			require.True(t, allowed.Admit, "sanity: first-party content is exempt from review")

			fx.rejectRef(tc.ref)
			g2 := &contentGate{cfg: cfg, records: fx.records()}
			v := g2.Admit(bundles.Exposure{Read: tc.read, Ref: tc.ref, Bytes: []byte("KEEPER-PAYLOAD"), Form: bundles.FormRaw})

			assert.False(t, v.Admit, "a human's rejection must reach %s content", tc.name)
			assert.Equal(t, bundles.ReasonRejected, v.Reason)
		})
	}
}

// builtinLikeRead / companionLikeRead present the fixture bundle with the two
// non-project first-party postures. They go through the readers that own those
// postures rather than minting them: a builtin is read out of the binary's own
// embedded FS, a companion off the stdout of a binary the user consented to run.
func builtinLikeRead(t *testing.T) bundles.BundleRead {
	t.Helper()
	read := bundles.ProjectAuthoredRead("kit", authorizerBundle())
	read.Provenance = bundles.ProvenanceBuiltin
	return read
}

func companionLikeRead(t *testing.T) bundles.BundleRead {
	t.Helper()
	read := bundles.ProjectAuthoredRead("ltk", authorizerBundle())
	read.Provenance = bundles.ProvenanceCompanion
	return read
}

// --- companion: an unverifiable signature REPORTS, it does not withhold -----

// A companion loadout arrives on the stdout of a binary the user already
// consented to execute; there is no intermediary for a publisher signature to
// protect against. A signature that fails to verify there is a stale or
// mismatched signature in the companion's own release — a bug signal, not an
// attack signal — so the content is DELIVERED and the fact is reported.
//
// This is the sentence phase A reversed. Only "never crashes" survived of the
// old "a companion loadout from a companion is withheld, never crashes, never
// auto-allowed" line; see docs/trust-model.md.
func TestAuthorizer_CompanionInvalidSignatureIsDeliveredAndReported(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}

	// A companion read whose signature does not cover its bytes: local posture
	// (it never crossed an intermediary), companion provenance, invalid signature.
	read := staleLocalRead(t, "ltk")
	read.Provenance = bundles.ProvenanceCompanion
	ref := trust.Ref{RepoURL: "ctxloom:companion", Bundle: "ltk", Kind: trust.KindFragment, Name: "keeper", IsCompanion: true}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	v := bundles.Decide(g, read, ref.ItemRef(), []byte("KEEPER-PAYLOAD"), bundles.FormRaw)

	assert.True(t, v.Admit, "a companion's unverifiable signature must NOT withhold its content")
	assert.Equal(t, bundles.ReasonStaleLocalSignature, v.Reason)
	assert.NotEmpty(t, v.Detail, "and the fact must be reportable")
	assert.NotEmpty(t, warnings.String(), "and actually reported — a Detail nobody prints is a fact nobody learns")
}

// --- local | invalid: ADMIT + WARN, and the warning is actually emitted -----

// checkLocalSignature's stale-sidecar diagnostic, as the decision table's
// `local | invalid | *` row. The content arrives (locality already answered the
// trust question) and the AUTHOR IS TOLD, at the moment their bundle stopped
// being publishable rather than at `bundle push` time.
func TestAuthorizer_StaleLocalSignatureAdmitsAndTheAuthorIsTold(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}
	read := staleLocalRead(t, "stale-kit")

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	t.Cleanup(restore)

	v := admitFragment(t, g, read, "stale-kit#fragments/keeper", "KEEPER-PAYLOAD")

	require.True(t, v.Admit, "a stale sidecar over LOCAL bytes must never withhold — there is nothing to gate")
	assert.Equal(t, bundles.ReasonStaleLocalSignature, v.Reason)
	assert.True(t, v.Warns(), "the verdict must announce that it carries something to say")
	assert.Contains(t, v.Detail, "stale-kit.yaml.sig")
	assert.Contains(t, v.Detail, "ctxloom bundle sign stale-kit", "the warning must name the command that fixes it")
	assert.Contains(t, warnings.String(), "stale-kit.yaml.sig",
		"and bundles.Decide must have EMITTED it: the authorizer is pure, the caller speaks")
}

// staleLocalRead reads a project bundle whose sibling `.sig` was made over
// DIFFERENT bytes — an author who edited and did not re-sign. Real signing, so
// "the signature does not cover these bytes" is a fact rather than a fixture
// convention.
func staleLocalRead(t *testing.T, name string) bundles.BundleRead {
	t.Helper()
	fsys := afero.NewMemMapFs()
	body := []byte("version: \"1.0\"\nfragments:\n  keeper:\n    content: KEEPER-PAYLOAD\n")
	sig, root := signAs(t, body, "author@example.test")
	edited := append(append([]byte{}, body...), []byte("# edited, never re-signed\n")...)
	require.NoError(t, afero.WriteFile(fsys, "/bundles/"+name+".yaml", edited, 0o644))
	require.NoError(t, afero.WriteFile(fsys, "/bundles/"+name+".yaml"+bundles.SigSuffix, sig, 0o644))

	loader := bundles.NewLoader(bundles.NewProjectReader(fsys, []string{"/bundles"}, bundles.WithTrustRoot(root)))
	read := readOf(t, loader, name)
	require.Equal(t, bundles.SignatureInvalid, read.Signature(), "fixture must actually be stale")
	require.Equal(t, bundles.TrustCtxLocal, read.TrustCtx())
	return read
}

// --- fail-closed on unset ---------------------------------------------------

// Zero means UNSET on every axis, and unset means withhold. An Exposure built
// from a struct literal establishes nothing, and must never read as "local,
// unsigned, no signer" — which is exactly the claim a zero value would
// otherwise make.
func TestAuthorizer_UnclaimedReadWithholds(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}

	v := g.Admit(bundles.Exposure{
		Read:  bundles.BundleRead{},
		Ref:   trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsLocal: true},
		Bytes: []byte("KEEPER-PAYLOAD"),
		Form:  bundles.FormRaw,
	})

	assert.False(t, v.Admit, "an unclaimed read must withhold even for a ref that spells 'local'")
	assert.Equal(t, bundles.ReasonUnestablished, v.Reason)
	assert.NotEmpty(t, v.Detail)
}

// The cascade's own half of the same rule: an UNSET posture reaches no
// first-party arm at all, so it falls out the terminal fail-closed default.
// A caller that cannot state where content came from gets LESS exposure.
func TestEffectiveTrust_UnsetPostureWithholds(t *testing.T) {
	ref := trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsLocal: true}

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: ref, Payload: pbytes("x"), Form: rawForm, Records: fakeRecords{},
		// Posture and Provenance deliberately left zero.
	})

	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision,
		"unset is not 'local': a ref that SPELLS local must not be allowed when no reader said so")
	assert.Equal(t, trust.SourcePending, res.Source)
}

// A contradictory pair — local posture, remote provenance — is not a fourth
// exemption. It matches no arm and falls through fail-closed.
func TestEffectiveTrust_ContradictoryPostureWithholds(t *testing.T) {
	ref := trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsLocal: true}

	res, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: ref, Payload: pbytes("x"), Form: rawForm, Records: fakeRecords{},
		Posture: bundles.TrustCtxLocal, Provenance: bundles.ProvenanceRemote,
	})

	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
}

// --- the gate takes BYTES, not a hash ---------------------------------------

// The exposure carries the EXACT bytes about to be delivered, so the decision
// can VERIFY rather than merely compare. A hash can only ever be checked against
// a recorded hash, and a recorded hash is a file anything can write (spec §9.3,
// trap #2).
//
// Asserted through the consequence, not the type: an approval of one set of
// bytes must not admit a different set under the same ref. A hash-keyed gate
// whose index was edited would.
func TestAuthorizer_DecidesOnBytesSoChangedContentReGates(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	read := readOf(t, seedLoader(t, map[string]*bundles.Bundle{authorizerRemoteRef: authorizerBundle()}), authorizerRemoteRef)
	itemRef := authorizerRemoteRef + "#fragments/keeper"
	tRef, _, _, err := trust.ParseItemRef(itemRef)
	require.NoError(t, err)
	fx.approve(tRef, signing.FormRaw, []byte("KEEPER-PAYLOAD"))

	g := &contentGate{cfg: cfg, records: fx.records()}

	approved := admitFragment(t, g, read, itemRef, "KEEPER-PAYLOAD")
	assert.True(t, approved.Admit, "the approved bytes are delivered")
	assert.Equal(t, bundles.ReasonApproved, approved.Reason)

	swapped := admitFragment(t, g, read, itemRef, "SUBSTITUTED-PAYLOAD")
	assert.False(t, swapped.Admit, "different bytes under the same ref must re-gate")
	assert.Equal(t, bundles.ReasonUnsigned, swapped.Reason,
		"and land where unsigned remote content lands: awaiting review")

	// The exposure the authorizer received is the payload itself. If it were a hash
	// the two calls above could not have differed without the caller hashing —
	// which is the indirection the design removed.
	var got []byte
	bundles.Decide(bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		got = e.Bytes
		return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
	}), read, itemRef, []byte("KEEPER-PAYLOAD"), bundles.FormRaw)
	assert.Equal(t, []byte("KEEPER-PAYLOAD"), got, "the authorizer is handed BYTES, never a hash")
}

// --- one verdict serves the gate and the report -----------------------------

// `ctxloom review` must not present tampered remote content as ordinary
// unsigned content awaiting a look: a report that re-derived its own answer
// from a bundle's signer stamps could not express "invalid", and would offer a
// human the chance to approve exactly the bytes the delivery path refused.
func TestPendingReview_TamperedRemoteIsNotOfferedForReview(t *testing.T) {
	fx := newTrustFixture(t)
	loader := seedTampered(t, reviewSeedKey, "publisher@example.test", reviewBundle())

	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t),
		Loader:   loader,
		FS:       afero.NewMemMapFs(),
	})

	require.NoError(t, err)
	assert.Zero(t, res.Total,
		"tampered bytes must never reach the review queue — approving them is the §10.2 downgrade completing itself")
}

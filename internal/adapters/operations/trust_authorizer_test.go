package operations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/content/attest"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The rows the decision table keys on the READ for, decided by the production
// Authorizer (contentGate.Admit) rather than by a spelled-out fixture.
//
// These are the rules phase A left in bundles.Loader.admit with a comment naming
// this slice as their removal point. The loader could only DROP: a bundle it
// withheld was unaddressable everywhere, with no reason a user could act on and
// no way for a review surface to see the same fact. Here they are verdicts.

const authorizerRemoteRef = "https://example.test/repo@bundles/kit"

// authorizerItemRef is authorizerRemoteRef's fragment "keeper", in the
// canonical bundle-reference grammar — the shape bundles.Decide itself now
// parses. Every admitFragment/bundles.Decide call below feeds it directly
// (these tests exercise Decide's own contract by hand, not through a
// producer), and every fx.approve/fx.rejectRef call parses it back with
// mustParseProducerRef so the write and the read key on the SAME identity.
var authorizerItemRef = mustGitItemRef("example.test", "/repo", "kit", trust.KindFragment, "keeper")

// authorizerBundle is the fixture content every test below decides about: one
// fragment, so the exposure carries real bytes.
func authorizerBundle() *bundles.Bundle {
	return &bundles.Bundle{Version: "1.0", Fragments: map[string]bundles.BundleFragment{"keeper": {
		ItemBody: bundles.ItemBody{
			Content: "KEEPER-PAYLOAD",
		},
	}}}
}

// admitFragment runs one fragment through the authorizer with the given read and
// returns the verdict.
func admitFragment(t *testing.T, g *contentGate, read bundles.BundleRead, ref string, body string) bundles.Verdict {
	t.Helper()
	return bundles.Decide(report.Reporter{}, g, read, ref, []byte(body), bundles.FormRaw)
}

// --- remote tamper: REFUSED AT THE READ, never gated -----------------------
//
// These four tests used to drive the AUTHORIZER with a tampered remote read.
// They cannot any more, and the reason is the point rather than an obstacle: a
// remote bundle is a TREE, verified against its signed manifest at the read
// (repoFSReader.verifyTree), so content whose bytes no longer match what was
// signed never becomes a read and never reaches a gate. The property they
// pinned — tampered remote content must never reach a session — is unchanged;
// the place that enforces it moved earlier, so they assert it there.
//
// THIS REVERSES A DELIBERATE TRADE, and it should be read as the headline here
// rather than as a side effect. The approval-override test below carried this
// warning: "spec §10.2's downgrade is now reachable. Corrupting a publisher's
// .sig in the clone cache turns signed content into content a human may accept,
// and the acceptance prompt is where that attack lands." That attack no longer
// has a prompt to land on. The acceptance prompt is downstream of a read, and a
// tampered tree produces none.

// tamperedRemoteLoad drives a tampered remote tree through the loader and
// returns what the operator is told. No read comes back, so the diagnostic IS
// the observable.
func tamperedRemoteLoad(t *testing.T, ref string, b *bundles.Bundle) (*bundles.Loader, string) {
	t.Helper()
	var warnings strings.Builder
	restore := clidiag.SetSink(&warnings)
	defer restore()
	l := seedTampered(t, ref, "publisher@example.test", b)
	l.Reads() // force the read, which is where the refusal happens
	return l, warnings.String()
}

// The core property, at its new home: a tree whose files no longer match the
// manifest its publisher signed does not become content.
func TestRemoteTamperedTree_NeverBecomesAReadableBundle(t *testing.T) {
	l, warnings := tamperedRemoteLoad(t, authorizerRemoteRef, authorizerBundle())

	assert.Empty(t, l.Reads(), "tampered remote content must never become a readable bundle")
	_, err := l.Read(authorizerRemoteRef)
	require.Error(t, err, "and nothing may resolve it")

	assert.Contains(t, warnings, "does not match what was signed",
		"the operator must be told the content disagrees with the signature")
	assert.NotContains(t, warnings, "unattested",
		"a broken signature is not a missing one — collapsing the two IS the spec 10.2 downgrade")
}

// AN APPROVAL CANNOT RESCUE IT, which INVERTS the decision this test used to
// carry. A human's countersignature over exact bytes was allowed to stand above
// a publisher signature that no longer covered them, knowingly making the
// §10.2 downgrade reachable. A tampered tree now fails before any of that: there
// is no read to approve, so the approval has nothing to attach to.
//
// The human decision that approval covers BYTES is untouched — it still stands
// for every bundle that reads. What changed is that a tree which disagrees with
// its own signed manifest is no longer among them.
func TestRemoteTamperedTree_IsNotRescuedByAnApproval(t *testing.T) {
	fx := newTrustFixture(t)
	fx.approve(mustParseProducerRef(t, authorizerItemRef), signing.FormRaw, []byte("KEEPER-PAYLOAD"))

	l, _ := tamperedRemoteLoad(t, authorizerRemoteRef, authorizerBundle())

	assert.Empty(t, l.Reads(),
		"an approval of exactly these bytes must not resurrect a tree that disagrees with its signed manifest")
	_, err := l.Read(authorizerRemoteRef)
	require.Error(t, err)
}

// A REJECTION reaches the same outcome by a shorter route, and the ordering it
// pinned is now moot: rejection outranked tamper because both produced a read
// and something had to name the more actionable one. Neither produces a read
// now, so the user's own decision cannot be contradicted by a signature
// diagnosis — there is no diagnosis to contradict it with.
func TestRemoteTamperedTree_RejectionNeedsNoOrderingAgainstTamper(t *testing.T) {
	fx := newTrustFixture(t)
	fx.rejectRef(mustParseProducerRef(t, authorizerItemRef))

	l, _ := tamperedRemoteLoad(t, authorizerRemoteRef, authorizerBundle())

	assert.Empty(t, l.Reads(), "withheld either way; the precedence question no longer arises")
	_, err := l.Read(authorizerRemoteRef)
	require.Error(t, err)
}

// TestRemoteRead_CanNeverCarryAnInvalidSignature pins the CALLER INVARIANT that
// two removed branches rest on, which DECISIONS.md P12 requires before either
// may be removed: "removing a defensive branch whose unreachability rests on a
// caller invariant now requires a test pinning that invariant."
//
// The branches are bundles.PublisherOf's remote-tampered arm and
// operations.pendingReason's SignatureInvalid arm. Both answered the question
// "what do we say about a REMOTE read whose signature does not cover its
// bytes?", and both are gone because no such read can be produced: a remote
// bundle is a tree, and a tree that disagrees with its signed manifest is
// refused at the read.
//
// Without this test the removal is just a deletion. With it, a change that
// reintroduces remote SignatureInvalid — a new reader, or a relaxation of
// verifyTree — fails HERE, naming the invariant, instead of silently restoring
// a state whose handling no longer exists.
//
// It enumerates every outcome a remote read can reach, so it cannot pass by
// testing only the easy ones.
func TestRemoteRead_CanNeverCarryAnInvalidSignature(t *testing.T) {
	const ref = authorizerRemoteRef

	assertRemoteNotInvalid := func(t *testing.T, l *bundles.Loader, wantSig bundles.Signature) {
		t.Helper()
		read := readOf(t, l, ref)
		require.Equal(t, bundles.TrustCtxRemote, read.TrustCtx(), "the fixture must actually be remote")
		assert.NotEqual(t, bundles.SignatureInvalid, read.Signature(),
			"a REMOTE read must never carry SignatureInvalid: the branches that handled that state are gone")
		assert.Equal(t, wantSig, read.Signature())
	}

	t.Run("unsigned tree is none, not invalid", func(t *testing.T) {
		assertRemoteNotInvalid(t, seedLoader(t, map[string]*bundles.Bundle{ref: authorizerBundle()}), bundles.SignatureNone)
	})

	t.Run("signed by an untrusted key is valid, not invalid", func(t *testing.T) {
		l, _ := seedUntrustedSigned(t, ref, authorizerBundle())
		assertRemoteNotInvalid(t, l, bundles.SignatureValid)
	})

	t.Run("signed by a trusted key is valid", func(t *testing.T) {
		assertRemoteNotInvalid(t, seedTrustedSigned(t, ref, "publisher@example.test", authorizerBundle()), bundles.SignatureValid)
	})

	// The fourth outcome is the one that used to be SignatureInvalid, and the
	// whole invariant rests on it: signed-then-altered yields NO READ, so there
	// is no signature fact for anything downstream to mishandle.
	t.Run("signed then altered yields no read at all", func(t *testing.T) {
		l, _ := tamperedRemoteLoad(t, ref, authorizerBundle())
		assert.Empty(t, l.Reads(),
			"if this ever returns a read, SignatureInvalid is reachable again and the removed branches must come back")
	})
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
		{"companion", trust.Ref{RepoURL: "ctxloom:companion", Bundle: "ltk", Kind: trust.KindFragment, Name: "keeper", IsCompanion: true},
			companionLikeRead(t)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
			fx := newTrustFixture(t)

			g := &contentGate{cfg: cfg, records: fx.records()}
			allowed := g.Admit(bundles.Exposure{Read: tc.read, Ref: tc.ref, Bytes: []byte("KEEPER-PAYLOAD"), Form: bundles.FormRaw})
			require.True(t, allowed.Allow, "sanity: first-party content is exempt from review")

			fx.rejectRef(tc.ref)
			g2 := &contentGate{cfg: cfg, records: fx.records()}
			v := g2.Admit(bundles.Exposure{Read: tc.read, Ref: tc.ref, Bytes: []byte("KEEPER-PAYLOAD"), Form: bundles.FormRaw})

			assert.False(t, v.Allow, "a human's rejection must reach %s content", tc.name)
			assert.Equal(t, bundles.ReasonRejected, v.Reason)
		})
	}
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
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}

	// A companion read whose signature does not cover its bytes: local posture
	// (it never crossed an intermediary), companion provenance, invalid signature.
	read := staleLocalRead(t, "ltk")
	read.Provenance = bundles.ProvenanceCompanion
	ref := trust.Ref{RepoURL: "ctxloom:companion", Bundle: "ltk", Kind: trust.KindFragment, Name: "keeper", IsCompanion: true}

	var found report.Findings

	refStr, err := ref.DisplayRef()
	require.NoError(t, err)
	v := bundles.Decide(report.To(&found), g, read, refStr, []byte("KEEPER-PAYLOAD"), bundles.FormRaw)

	assert.True(t, v.Allow, "a companion's unverifiable signature must NOT withhold its content")
	assert.Equal(t, bundles.ReasonStaleLocalSignature, v.Reason)
	assert.NotEmpty(t, v.Detail, "and the fact must be reportable")
	assert.NotEmpty(t, found, "and actually reported — a Detail nobody reports is a fact nobody learns")
}

// --- local | invalid: ADMIT + WARN, and the warning is actually emitted -----

// checkLocalSignature's stale-sidecar diagnostic, as the decision table's
// `local | invalid | *` row. The content arrives (locality already answered the
// trust question) and the AUTHOR IS TOLD, at the moment their bundle stopped
// being publishable rather than at `bundle push` time.
func TestAuthorizer_StaleLocalSignatureAdmitsAndTheAuthorIsTold(t *testing.T) {
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}
	read := staleLocalRead(t, "stale-kit")

	var found report.Findings
	v := bundles.Decide(report.To(&found), g, read, mustLocalItemRef("stale-kit", trust.KindFragment, "keeper"), []byte("KEEPER-PAYLOAD"), bundles.FormRaw)

	require.True(t, v.Allow, "a stale manifest over LOCAL files must never withhold — there is nothing to gate")
	assert.Equal(t, bundles.ReasonStaleLocalSignature, v.Reason)
	assert.True(t, bundles.Warns(v), "the verdict must announce that it carries something to say")
	assert.Contains(t, v.Detail, content.ManifestPath)
	assert.Contains(t, v.Detail, "ctxloom bundle sign stale-kit", "the warning must name the command that fixes it")
	assert.Contains(t, strings.Join(found.Texts(), "\n"), content.ManifestPath,
		"and bundles.Decide must have EMITTED it: the authorizer is pure, the caller speaks")
}

// staleLocalRead reads a project bundle whose sibling `.sig` was made over
// DIFFERENT bytes — an author who edited and did not re-sign. Real signing, so
// "the signature does not cover these bytes" is a fact rather than a fixture
// convention.
func staleLocalRead(t *testing.T, name string) bundles.BundleRead {
	t.Helper()
	fsys := afero.NewMemMapFs()
	v2 := paths.BundlesLayoutRoot("/bundles", paths.LayoutV2)
	require.NoError(t, fsys.MkdirAll(v2, 0o755))
	st, err := content.NewTreeStore(fsys, v2, content.Provenance{IsLocal: true})
	require.NoError(t, err)
	require.NoError(t, st.Put(context.Background(),
		trust.Ref{Bundle: name, Kind: trust.KindFragment, Name: "keeper"},
		signing.FormRaw,
		content.Fragment{Name: "keeper", ItemMeta: content.ItemMeta{Body: "KEEPER-PAYLOAD"}}))
	require.NoError(t, st.PutRootFile(context.Background(), content.BundleID(name), bundles.DirectoryFormManifest,
		[]byte("version: \"1.0\"\n")))
	signer, root, _ := seedSigner(t, "author@example.test")
	tree, err := st.Open(context.Background(), content.BundleID(name))
	require.NoError(t, err)
	require.NoError(t, attest.SignBundle(context.Background(), st, tree, signer))
	// The author's edit after signing: the manifest no longer covers the file.
	keeper := filepath.Join(v2, name, "fragments", "keeper.md")
	testsupport.WriteFile(t, fsys, keeper, []byte("KEEPER-PAYLOAD\n# edited, never re-signed\n"), 0o644)

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
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}

	v := g.Admit(bundles.Exposure{
		Read:  bundles.BundleRead{},
		Ref:   trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "keeper", IsLocal: true},
		Bytes: []byte("KEEPER-PAYLOAD"),
		Form:  bundles.FormRaw,
	})

	assert.False(t, v.Allow, "an unclaimed read must withhold even for a ref that spells 'local'")
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
	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	read := readOf(t, seedLoader(t, map[string]*bundles.Bundle{authorizerRemoteRef: authorizerBundle()}), authorizerRemoteRef)
	itemRef := authorizerItemRef
	tRef := mustParseProducerRef(t, itemRef)
	fx.approve(tRef, signing.FormRaw, []byte("KEEPER-PAYLOAD"))

	g := &contentGate{cfg: cfg, records: fx.records()}

	approved := admitFragment(t, g, read, itemRef, "KEEPER-PAYLOAD")
	assert.True(t, approved.Allow, "the approved bytes are delivered")
	assert.Equal(t, bundles.ReasonApproved, approved.Reason)

	swapped := admitFragment(t, g, read, itemRef, "SUBSTITUTED-PAYLOAD")
	assert.False(t, swapped.Allow, "different bytes under the same ref must re-gate")
	assert.Equal(t, bundles.ReasonUnsigned, swapped.Reason,
		"and land where unsigned remote content lands: awaiting review")

	// The exposure the authorizer received is the payload itself. If it were a hash
	// the two calls above could not have differed without the caller hashing —
	// which is the indirection the design removed.
	var got []byte
	bundles.Decide(report.Reporter{}, bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		got = e.Bytes
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
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

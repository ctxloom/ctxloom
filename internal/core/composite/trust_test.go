package composite

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// The gate holder decides with three ports and NOTHING else: a trust root
// (whose key is trusted to publish), the review records (what a human
// approved or rejected) and the retraction records (what a publisher
// withdrew). These tests pin the default the holder can never lose — an
// executable item nothing positively justified is WITHHELD — and the one
// spelling of admit-everything, Ungated, which says so in its reason.

// fakeRoot trusts nothing: the holder is not the place a signer decision
// is made (the readers establish the signer axis), and these tests never
// reach it.
type fakeRoot struct{}

func (fakeRoot) TrustedForNamespace(ssh.PublicKey, string, time.Time) SignerDecision {
	return SignerDecision{Reason: "no key is trusted here"}
}

type fakeRecords struct {
	rejected func(trust.Ref, []byte) bool
	approved func(trust.Ref, []byte, bundles.ContentForm) bool
}

func (f fakeRecords) Rejected(ref trust.Ref, payload []byte) bool {
	if f.rejected == nil {
		return false
	}
	return f.rejected(ref, payload)
}

func (f fakeRecords) Approved(ref trust.Ref, payload []byte, form bundles.ContentForm) bool {
	if f.approved == nil {
		return false
	}
	return f.approved(ref, payload, form)
}

type fakeRetraction func(trust.BundleRef) (bool, string)

func (f fakeRetraction) Retracted(ref trust.BundleRef) (bool, string) {
	if f == nil {
		return false, ""
	}
	return f(ref)
}

func noRecords() fakeRecords       { return fakeRecords{} }
func noRetraction() fakeRetraction { return nil }
func mustTrust(t *testing.T, r ReviewRecords, x RetractionRecords) Trust {
	t.Helper()
	tr, err := NewTrust(fakeRoot{}, r, x)
	require.NoError(t, err)
	return tr
}

// remoteExecutable is an unsigned command pulled from a publisher's repo —
// the shape that must be withheld until a human reviews it.
func remoteExecutable(t *testing.T) (bundles.Exposure, string) {
	t.Helper()
	const refStr = "ctxloom+git://github.com/acme/repo//bundles/tools#prompts/deploy"
	br, err := trust.ParseBundleRef(refStr)
	require.NoError(t, err)
	b := &bundles.Bundle{Name: "tools"}
	read := bundles.NewRead("tools", b, bundles.ProvenanceRemote, bundles.TrustCtxRemote,
		bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone})
	return bundles.Exposure{
		Read:      read,
		BundleRef: br,
		Bytes:     []byte("#!/bin/sh\necho deploy\n"),
		Form:      bundles.FormRaw,
	}, refStr
}

func TestNewTrust_EveryPortRequired(t *testing.T) {
	_, err := NewTrust(nil, noRecords(), noRetraction())
	require.Error(t, err, "a holder with no trust root is not a gate")
	_, err = NewTrust(fakeRoot{}, nil, noRetraction())
	require.Error(t, err, "a holder with no review records is not a gate")
	_, err = NewTrust(fakeRoot{}, noRecords(), nil)
	require.Error(t, err, "a holder with no retraction records is not a gate")
}

func TestNewTrust_WithholdsAnExecutableNoReviewRecordApproves(t *testing.T) {
	tr := mustTrust(t, noRecords(), noRetraction())
	e, refStr := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "nothing positively justified this command")
	assert.Equal(t, bundles.ReasonUnsigned, v.Reason)
	assert.Contains(t, v.Detail, "no review record", "the withhold names what would admit it")
	assert.Equal(t, []string{refStr}, tr.Withheld(), "the holder tallies what it withheld, by ref")
	assert.True(t, tr.Gates())
}

func TestNewTrust_AReviewRecordAdmitsTheExecutable(t *testing.T) {
	e, _ := remoteExecutable(t)
	records := fakeRecords{approved: func(ref trust.Ref, payload []byte, form bundles.ContentForm) bool {
		return ref == e.Ref() && string(payload) == string(e.Bytes) && form == bundles.FormRaw
	}}
	tr := mustTrust(t, records, noRetraction())

	v := tr.Authorizer().Admit(e)

	assert.True(t, v.Allow)
	assert.Equal(t, bundles.ReasonApproved, v.Reason)
	assert.Empty(t, tr.Withheld())
}

func TestNewTrust_ARejectionOutranksAnApproval(t *testing.T) {
	e, _ := remoteExecutable(t)
	records := fakeRecords{
		approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true },
		rejected: func(trust.Ref, []byte) bool { return true },
	}
	tr := mustTrust(t, records, noRetraction())

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow)
	assert.Equal(t, bundles.ReasonRejected, v.Reason)
}

func TestNewTrust_ARetractionRefusesTheExecutable(t *testing.T) {
	e, _ := remoteExecutable(t)
	records := fakeRecords{approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true }}
	retraction := fakeRetraction(func(ref trust.BundleRef) (bool, string) {
		return ref.Bundle == "tools", "key compromised"
	})
	tr := mustTrust(t, records, retraction)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "a publisher's retraction beats a human's approval")
	assert.Equal(t, bundles.ReasonRetracted, v.Reason)
	assert.Equal(t, "key compromised", v.Detail)
}

func TestNewTrust_AnUnclaimedReadIsWithheld(t *testing.T) {
	tr := mustTrust(t, noRecords(), noRetraction())
	e, _ := remoteExecutable(t)
	e.Read = bundles.BundleRead{} // a struct literal claims nothing

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow)
	assert.Equal(t, bundles.ReasonUnestablished, v.Reason)
}

func TestNewTrust_AFaultedPortWithholdsEverythingAndNamesTheFault(t *testing.T) {
	e, _ := remoteExecutable(t)
	records := faultedRecords{fakeRecords{approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true }},
		errors.New("approvals store: permission denied")}
	tr := mustTrust(t, records, noRetraction())

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "a store that cannot be read approves nothing")
	assert.Equal(t, bundles.ReasonPending, v.Reason)
	assert.Contains(t, v.Detail, "permission denied")
}

// faultedRecords is a ReviewRecords whose backing store could not be read;
// it reports so through the optional Faulted capability.
type faultedRecords struct {
	fakeRecords
	err error
}

func (f faultedRecords) Fault() error { return f.err }

func TestUngated_IsTheOnlySpellingOfAdmitEverything(t *testing.T) {
	tr := Ungated()
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, tr.Gates())
	assert.True(t, v.Allow)
	assert.Equal(t, bundles.ReasonUngated, v.Reason, "an ungated admit names the absence of a rule")
	assert.Empty(t, tr.Withheld())
}

func TestTrust_ZeroValue_HasNoPermissiveAnswer(t *testing.T) {
	var zero Trust
	require.Nil(t, zero.Authorizer(), "a zero Trust holds no gate: the nil authorizer is the spelling bundles.Decide withholds on loudly")
	assert.True(t, zero.Gates(), "a zero Trust is not an ungated surface; it is a surface that forgot its gate")
}

// --- the locality rule ------------------------------------------------------

// invalidlySigned is a bundle read whose signature does not cover its files
// — an author's edit after signing — in the given trust context and
// provenance.
func invalidlySigned(t *testing.T, refStr string, ctx bundles.TrustCtx, prov bundles.ProvenanceClass) bundles.Exposure {
	t.Helper()
	br, err := trust.ParseBundleRef(refStr)
	require.NoError(t, err)
	b := &bundles.Bundle{Name: br.Bundle}
	read := bundles.NewRead(br.Bundle, b, prov, ctx,
		bundles.SignatureFacts{Signature: bundles.SignatureInvalid, Signer: bundles.SignerUntrusted, Detail: "its files no longer match SHA256SUMS"})
	return bundles.Exposure{Read: read, BundleRef: br, Bytes: []byte("echo deploy"), Form: bundles.FormRaw}
}

// TestNewTrust_LocalityRule_AProjectLocalBundleWithAnInvalidSignatureIsAdmittedAsUnsigned:
// locality is the trust boundary for a bundle the human already controls
// under source control; the signature is for what travels. An author editing
// a bundle in place breaks its signature on every keystroke, and refusing it
// would make local iteration impossible — so the signature is treated as
// absent, the admit reason stays ReasonStaleLocalSignature so the surface
// can warn, and the author is told how to re-sign.
func TestNewTrust_LocalityRule_AProjectLocalBundleWithAnInvalidSignatureIsAdmittedAsUnsigned(t *testing.T) {
	tr := mustTrust(t, noRecords(), noRetraction())
	e := invalidlySigned(t, "ctxloom+local:tools#prompts/deploy", bundles.TrustCtxLocal, bundles.ProvenanceProject)

	v := tr.Authorizer().Admit(e)

	assert.True(t, v.Allow, "locality already answered the trust question")
	assert.Equal(t, bundles.ReasonStaleLocalSignature, v.Reason)
	assert.True(t, bundles.Warns(v), "the verdict carries the warning the surface prints")
	assert.Contains(t, v.Detail, "ctxloom bundle sign tools", "the author is told how to re-sign")
	assert.Empty(t, tr.Withheld())
}

// TestNewTrust_LocalityRule_TheSameBundleFromARemoteSourceIsWithheld: the
// same facts over content that TRAVELLED admit nothing — the signature is
// for what travels, and one that does not cover its bytes justifies no
// exposure. (The reader adapters refuse such a tree before it becomes a
// read at all; the gate withholds it if one ever arrives.)
func TestNewTrust_LocalityRule_TheSameBundleFromARemoteSourceIsWithheld(t *testing.T) {
	tr := mustTrust(t, noRecords(), noRetraction())
	e := invalidlySigned(t, "ctxloom+git://github.com/acme/repo//bundles/tools#prompts/deploy", bundles.TrustCtxRemote, bundles.ProvenanceRemote)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "a signature that does not cover what travelled admits nothing")
	assert.NotEqual(t, bundles.ReasonStaleLocalSignature, v.Reason, "stale-local is a LOCAL row and never names remote content")
	assert.Equal(t, []string{e.RefString()}, tr.Withheld())
}

// faultedRetraction is a RetractionRecords whose backing lockfile could not be
// read; it reports so through the optional Faulted capability.
type faultedRetraction struct {
	fakeRetraction
	err error
}

func (f faultedRetraction) Fault() error { return f.err }

// companionFragment is a fragment from an installed companion's own loadout
// (ctxloom's own included) — the shape no lockfile entry can retract.
func companionFragment(t *testing.T) bundles.Exposure {
	t.Helper()
	const refStr = "ctxloom+companion:ctxloom#fragments/isolation-axes"
	br, err := trust.ParseBundleRef(refStr)
	require.NoError(t, err)
	b := &bundles.Bundle{Name: "ctxloom:companion@ctxloom"}
	read := bundles.NewRead("ctxloom:companion@ctxloom", b, bundles.ProvenanceCompanion, bundles.TrustCtxLocal,
		bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone})
	return bundles.Exposure{
		Read:      read,
		BundleRef: br,
		Bytes:     []byte("Set both isolation axes."),
		Form:      bundles.FormRaw,
	}
}

// TestNewTrust_AnUnreadableLockfileDoesNotWithholdCompanionContent: a
// retraction is a PUBLISHER's withdrawal of a bundle, sourced from the
// lockfile — and a companion loadout has no lockfile entry, so no retraction
// can cover it and an unreadable lockfile has nothing to say about it. A
// project-less start with a broken home lockfile must still get ctxloom's
// own loadout (its MCP server, its guidance), which is companion content.
// Remote content in the same state stays withheld (the sibling test above).
func TestNewTrust_AnUnreadableLockfileDoesNotWithholdCompanionContent(t *testing.T) {
	retraction := faultedRetraction{nil, errors.New("lock.yaml is unreadable")}
	tr := mustTrust(t, noRecords(), retraction)

	v := tr.Authorizer().Admit(companionFragment(t))
	assert.True(t, v.Allow, "a companion loadout cannot be retracted, so an unreadable lockfile cannot withhold it")
	assert.Equal(t, bundles.ReasonCompanion, v.Reason)

	remote, _ := remoteExecutable(t)
	rv := tr.Authorizer().Admit(remote)
	assert.False(t, rv.Allow, "control: remote content under an unreadable lockfile is still withheld")
	assert.Equal(t, bundles.ReasonPending, rv.Reason)
}

// An Exposure whose identity was never set is withheld as unaddressable, even
// when every port would admit it. Any literal that omits BundleRef produces
// exactly this value, so this one test pins the fail-closed answer for every
// future literal: nothing can key a rejection or a retraction on no bundle.
func TestAdmit_AnExposureNamingNoBundleIsWithheld(t *testing.T) {
	e, _ := remoteExecutable(t)
	e.BundleRef = trust.BundleRef{}
	approveAll := fakeRecords{approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true }}
	retractAll := fakeRetraction(func(trust.BundleRef) (bool, string) { return true, "withdrawn" })

	for name, x := range map[string]fakeRetraction{"no retraction": noRetraction(), "retract everything": retractAll} {
		t.Run(name, func(t *testing.T) {
			v := mustTrust(t, approveAll, x).Authorizer().Admit(e)
			assert.False(t, v.Allow, "zero identity admitted: %+v", v)
			assert.Equal(t, bundles.ReasonUnaddressable, v.Reason)
		})
	}
}

// A bundle-level (item-less) identity is still an identity: the zero-identity
// withhold must not reach it, or companion and bundle-level decisions would
// stop being made.
func TestAdmit_ABundleLevelIdentityIsNotUnaddressable(t *testing.T) {
	e, _ := remoteExecutable(t)
	br, err := trust.ParseBundleRef("ctxloom+companion:ltk")
	require.NoError(t, err)
	e.BundleRef = br
	approveAll := fakeRecords{approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true }}
	v := mustTrust(t, approveAll, noRetraction()).Authorizer().Admit(e)
	assert.NotEqual(t, bundles.ReasonUnaddressable, v.Reason, "%+v", v)
}

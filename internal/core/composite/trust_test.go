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

type fakeRetraction func(trust.Ref) (bool, string)

func (f fakeRetraction) Retracted(ref trust.Ref) (bool, string) {
	if f == nil {
		return false, ""
	}
	return f(ref)
}

func noRecords() fakeRecords         { return fakeRecords{} }
func noRetraction() fakeRetraction   { return nil }
func fixedNow() time.Time            { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) }
func mustTrust(t *testing.T, r ReviewRecords, x RetractionRecords) Trust {
	t.Helper()
	tr, err := NewTrust(fakeRoot{}, r, x, fixedNow)
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
		Read:   read,
		Ref:    trust.RefFromBundleRef(br),
		RefStr: refStr,
		Bytes:  []byte("#!/bin/sh\necho deploy\n"),
		Form:   bundles.FormRaw,
	}, refStr
}

func TestNewTrust_EveryPortRequired(t *testing.T) {
	_, err := NewTrust(nil, noRecords(), noRetraction(), fixedNow)
	require.Error(t, err, "a holder with no trust root is not a gate")
	_, err = NewTrust(fakeRoot{}, nil, noRetraction(), fixedNow)
	require.Error(t, err, "a holder with no review records is not a gate")
	_, err = NewTrust(fakeRoot{}, noRecords(), nil, fixedNow)
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
		return ref == e.Ref && string(payload) == string(e.Bytes) && form == bundles.FormRaw
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
	retraction := fakeRetraction(func(ref trust.Ref) (bool, string) {
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

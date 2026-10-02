package composite

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// --disable-sig-check waives the SIGNATURE step of the cascade and nothing
// else: every port is still required, and every refusal that outranks the
// signature step still refuses.

func waivedTrust(t *testing.T, r ReviewRecords, x RetractionRecords) Trust {
	t.Helper()
	tr, err := NewTrust(fakeRoot{}, r, x, WithoutSignatureCheck())
	require.NoError(t, err)
	return tr
}

func TestWithoutSignatureCheck_AdmitsUnsignedRemoteContentAndTheReasonNamesTheSwitch(t *testing.T) {
	tr := waivedTrust(t, noRecords(), noRetraction())
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.True(t, v.Allow, "with the signature check waived, an unsigned remote item is admitted")
	assert.Equal(t, bundles.ReasonSigCheckDisabled, v.Reason)
	assert.Contains(t, v.Reason.Explain(v.Detail), "--"+bundles.SigCheckFlag, "the decision reason names the flag")
	assert.Contains(t, v.Reason.Explain(v.Detail), bundles.SigCheckEnv, "and the env switch")
	assert.True(t, tr.SignatureCheckDisabled())
	assert.Empty(t, withheldRefsOf(tr))
}

func TestWithoutSignatureCheck_AdmitsAnUntrustedSignersContent(t *testing.T) {
	tr := waivedTrust(t, noRecords(), noRetraction())
	e, _ := remoteExecutable(t)
	e.Read = bundles.NewRead("tools", &bundles.Bundle{Name: "tools"}, bundles.ProvenanceRemote, bundles.TrustCtxRemote,
		bundles.SignatureFacts{Signature: bundles.SignatureValid, Signer: bundles.SignerUntrusted})

	v := tr.Authorizer().Admit(e)

	assert.True(t, v.Allow)
	assert.Equal(t, bundles.ReasonSigCheckDisabled, v.Reason)
}

func TestWithoutSignatureCheck_IsOffByDefault(t *testing.T) {
	tr := mustTrust(t, noRecords(), noRetraction())
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "control: without the option the same item is withheld")
	assert.Equal(t, bundles.ReasonUnsigned, v.Reason)
	assert.False(t, tr.SignatureCheckDisabled())
	assert.False(t, Trust{}.SignatureCheckDisabled(), "a zero Trust waives nothing")
}

func TestWithoutSignatureCheck_ARejectionStillRefuses(t *testing.T) {
	tr := waivedTrust(t, fakeRecords{rejected: func(trust.Ref, []byte) bool { return true }}, noRetraction())
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "a rejected item stays rejected with the signature check off")
	assert.Equal(t, bundles.ReasonRejected, v.Reason)
}

func TestWithoutSignatureCheck_ARetractionStillRefuses(t *testing.T) {
	tr := waivedTrust(t, noRecords(), fakeRetraction(func(trust.BundleRef) (bool, string) { return true, "withdrawn" }))
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow)
	assert.Equal(t, bundles.ReasonRetracted, v.Reason)
}

func TestWithoutSignatureCheck_AnUnreadableApprovalsStoreStillRefuses(t *testing.T) {
	tr := waivedTrust(t, faultedRecords{noRecords(), errors.New("approvals store is missing")}, noRetraction())
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow, "a store that cannot say what was rejected cannot be waived past")
	assert.Equal(t, bundles.ReasonRecordsUnreadable, v.Reason)
}

func TestWithoutSignatureCheck_AnApprovalStillNamesItself(t *testing.T) {
	approved := fakeRecords{approved: func(trust.Ref, []byte, bundles.ContentForm) bool { return true }}
	tr := waivedTrust(t, approved, noRetraction())
	e, _ := remoteExecutable(t)

	v := tr.Authorizer().Admit(e)

	assert.Equal(t, bundles.ReasonApproved, v.Reason, "a real approval is reported as one, not as the waiver")
}

func TestWithoutSignatureCheck_LeavesNonRemoteReadsAlone(t *testing.T) {
	tr := waivedTrust(t, noRecords(), noRetraction())
	e, _ := remoteExecutable(t)
	// A local-posture read that reached the pending arm was denied by
	// something other than a signature; the waiver has nothing to say.
	e.Read = bundles.NewRead("tools", &bundles.Bundle{Name: "tools"}, bundles.ProvenanceRemote, bundles.TrustCtxLocal,
		bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone})

	v := tr.Authorizer().Admit(e)

	assert.False(t, v.Allow)
	assert.Equal(t, bundles.ReasonPending, v.Reason)
}

func TestWithoutSignatureCheck_LeavesTheTrustRootAlone(t *testing.T) {
	tr := waivedTrust(t, noRecords(), noRetraction())
	assert.Equal(t, fakeRoot{}, tr.Root(), "companion admission and the readers verify against an unchanged root")
}

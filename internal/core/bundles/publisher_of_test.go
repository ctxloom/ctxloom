package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An invalid signature means two different things by trust context: on a
// remote read it is an edited signed tree (tampering, which only a waived
// generation's reader carries this far); on a local read it is an author who
// edited and did not re-sign.
func TestPublisherOf_NamesAnInvalidRemoteSignatureTampered(t *testing.T) {
	read := func(ctx TrustCtx) BundleRead {
		return NewRead("tools", &Bundle{Name: "tools"}, ProvenanceRemote, ctx,
			SignatureFacts{Signature: SignatureInvalid, Signer: SignerTrusted})
	}
	assert.Equal(t, ReasonTampered, PublisherOf(read(TrustCtxRemote)))
	assert.Equal(t, ReasonStaleLocalSignature, PublisherOf(read(TrustCtxLocal)))
}

// EditedSignedTrees names exactly the remote reads carried with an invalid
// signature — the installed signed trees a waived generation accepted although
// they were edited — sorted, so doctor and the dry run print a stable list.
func TestEditedSignedTrees_NamesOnlyRemoteInvalidReads(t *testing.T) {
	read := func(ref string, ctx TrustCtx, sig Signature) BundleRead {
		return NewRead(ref, &Bundle{Name: ref}, ProvenanceRemote, ctx, SignatureFacts{Signature: sig, Signer: SignerTrusted})
	}
	reads := []BundleRead{
		read("mid", TrustCtxRemote, SignatureInvalid),
		read("zeta", TrustCtxRemote, SignatureInvalid),
		read("alpha", TrustCtxRemote, SignatureInvalid),
		read("local-stale", TrustCtxLocal, SignatureInvalid),
		read("signed", TrustCtxRemote, SignatureValid),
		read("unsigned", TrustCtxRemote, SignatureNone),
	}
	assert.Equal(t, []string{"alpha", "mid", "zeta"}, EditedSignedTrees(reads))
	assert.Empty(t, EditedSignedTrees(nil))
}

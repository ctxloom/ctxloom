package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// EditedSignedTrees names exactly the remote reads carried with an invalid
// signature — the installed signed trees a waived generation accepted although
// they were edited — sorted, so doctor and the dry run print a stable list.
func TestEditedSignedTrees_NamesOnlyRemoteInvalidReads(t *testing.T) {
	read := func(ref string, ctx TrustCtx, sig Signature) BundleRead {
		return newRead(ref, &Bundle{Name: ref}, ProvenanceRemote, ctx, SignatureFacts{Signature: sig, Signer: SignerTrusted})
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

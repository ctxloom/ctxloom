package trust

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// Ref.Bundle and Ref.Name are set directly — Ref is a plain struct, and every
// surface type's RefFor in internal/content fills them from a bundle-manifest
// item name or a filename, neither of which goes through the reference grammar
// in internal/remote. A bundle pulled from a remote repo can therefore name a
// fragment with a control character in it.
//
// Key is where those fields become a ref string, and
// operations.countersignRef composes that string straight into the countersign
// preimage. So Key is the ingest boundary for this path.
func TestRefKey_StripsControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name, bundle, item, want string
	}{
		{
			name:   "forged header tail in the item name",
			bundle: "code-quality",
			item:   "solid\nform: fragment/raw\nlen: 15\n",
			want:   "code-quality#fragments/solidform: fragment/rawlen: 15",
		},
		{
			name:   "control character in the bundle name",
			bundle: "code\rquality",
			item:   "solid",
			want:   "codequality#fragments/solid",
		},
		{
			name:   "clean ref is untouched",
			bundle: "code-quality",
			item:   "solid",
			want:   "code-quality#fragments/solid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Ref{Bundle: tc.bundle, Kind: KindFragment, Name: tc.item}
			assert.Equal(t, tc.want, r.Key())
		})
	}
}

// TestRefCanonicalURL_StripsControlCharacters covers the other half of the
// countersign ref (CanonicalURL + "|" + Key): a repo URL routes through
// remote.NormalizeURL, which normalises at its own entry.
func TestRefCanonicalURL_StripsControlCharacters(t *testing.T) {
	r := Ref{RepoURL: "https://github.com/acme/repo\nevil", Bundle: "b", Kind: KindFragment, Name: "n"}
	assert.NotContains(t, r.CanonicalURL(), "\n")

	local := Ref{IsLocal: true}
	assert.Equal(t, remote.LocalSource, local.CanonicalURL())

	builtin := Ref{IsBuiltin: true}
	assert.Equal(t, BuiltinSigner, builtin.CanonicalURL())
}

package ident

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/refuri"
)

// Ref.Bundle and Ref.Name are set directly — Ref is a plain struct, and every
// surface type's RefFor in internal/adapters/content fills them from a bundle-manifest
// item name or a filename, neither of which goes through the reference grammar
// in internal/adapters/remote. A bundle pulled from a remote repo can therefore name a
// fragment with a control character in it.
//
// Key is where those fields become a ref string, so Key is the ingest
// boundary for this path.
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

// TestRefCanonicalURL_StripsControlCharacters covers the repository half of a
// ref's rendering (Key covers the item half): a repo URL routes through
// refuri.NormalizeURL, which normalises at its own entry, so a control
// character cannot reach the line-delimited text a canonical URL is written
// into (lockfile keys, terminal output).
func TestRefCanonicalURL_StripsControlCharacters(t *testing.T) {
	r := Ref{RepoURL: "https://github.com/acme/repo\nevil", Bundle: "b", Kind: KindFragment, Name: "n"}
	assert.NotContains(t, r.CanonicalURL(), "\n")

	local := Ref{IsLocal: true}
	assert.Equal(t, refuri.LocalSource, local.CanonicalURL())

}

// ParseSelector is the parse that turns a raw "<kind>/<name>" into a typed
// item, so it returns the NORMALISED name: a caller holding the parsed value
// has no reason to reach back for the raw text, and the raw text is where a
// control byte would ride through to a terminal or a ref string.
func TestParseSelector_ReturnsTheNormalisedName(t *testing.T) {
	const raw = "ev\x1b[2Jil"
	kind, name, err := ParseSelector(KindFragment.Dir() + "/" + raw)
	if err != nil {
		t.Fatalf("ParseSelector: %v", err)
	}
	assert.Equal(t, KindFragment, kind)
	assert.Equal(t, refuri.NormalizeRef(raw), name, "the parsed name is the normalised one")
}

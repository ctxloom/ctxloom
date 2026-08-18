package bundles

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCatalogInfos_ListsTheResolvableRefNotTheLeafName pins the listing's
// handle: what `bundle list` prints is what `bundle show`/`bundle remove` must
// accept, and only the read's ref resolves (Catalog.Lookup keys by it).
//
// A bundle below the top level is where this is decided. lang/go.yaml resolves
// as "lang/go" while its Bundle.Name is the leaf "go", so projecting Bundle.Name
// into the listing would print a handle that resolves to nothing — the same
// defect as listing a builtin nobody can remove, in a different place. The test
// asserts BOTH facts so the two names cannot quietly become one.
func TestCatalogInfos_ListsTheResolvableRefNotTheLeafName(t *testing.T) {
	loader := NewLoader(projectReaderOver(t, "lang/go.yaml", "version: 1.0.0\n"))

	infos, err := loader.List()
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "lang/go", infos[0].Name, "a listing must print the ref the user can type back")

	_, err = loader.Load(infos[0].Name)
	assert.NoError(t, err, "every name a listing prints must resolve")
}

// TestCatalogScoped_ExcludesBuiltinsAndKeepsAcquiredContent is the listing
// contract `bundle list` states in its own help: local plus pinned-remote
// content. A builtin ships inside the binary and is neither.
//
// The builtin here is the REAL embedded one (NewBuiltinReader over
// resources/builtin_bundles), not a fixture, so the assertion cannot pass by the
// builtin never having been there — the absence-satisfies-absence shape. The
// test proves it was present in the unscoped set first, then that scoping drops
// exactly it and keeps the remote and companion reads.
func TestCatalogScoped_ExcludesBuiltinsAndKeepsAcquiredContent(t *testing.T) {
	unsigned := signatureFacts{signature: SignatureNone, signer: SignerNone}
	acquired := staticReader{reads: []BundleRead{
		newRead("https://example.test/repo@bundles/pinned", &Bundle{Name: "https://example.test/repo@bundles/pinned", Version: "1.0.0"},
			ProvenanceRemote, TrustCtxRemote, unsigned),
		newRead(companionRefPrefix+"ltk", &Bundle{Name: companionRefPrefix + "ltk", Version: "1.0.0"},
			ProvenanceCompanion, TrustCtxLocal, unsigned),
	}}
	cat := Resolve(context.Background(),
		projectReaderOver(t, "local.yaml", "version: 1.0.0\n"),
		NewBuiltinReader(),
		acquired,
	)

	const builtinRef = "builtin:isolation"
	require.Contains(t, namesOf(cat.Infos()), builtinRef,
		"guard: the embedded builtin must be in the unscoped set, or this test proves nothing")

	got := namesOf(cat.Scoped(ProvenanceProject, ProvenanceRemote, ProvenanceCompanion).Infos())

	assert.NotContains(t, got, builtinRef, "a builtin is not installed and must not be listed")
	assert.Contains(t, got, "local", "project content must still be listed")
	assert.Contains(t, got, "https://example.test/repo@bundles/pinned", "pinned remote content must still be listed")
	assert.Contains(t, got, companionRefPrefix+"ltk", "companion content must still be listed")
	assert.Len(t, got, 3, "scoping drops the builtin and nothing else")
}

func namesOf(infos []*BundleInfo) []string {
	out := make([]string, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}

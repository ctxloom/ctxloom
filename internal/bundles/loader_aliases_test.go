package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Seeded bundles are keyed by their canonical ref — the sole resolution
// identity. Load resolves that key, and List enumerates it under that name.
func TestLoader_SeededCanonical_ResolvesAndLists(t *testing.T) {
	b := &Bundle{
		Version:   "1.0.0",
		Fragments: map[string]BundleFragment{"security": {Content: "SEC"}},
	}
	const canonical = "https://github.com/ctxloom/ctxloom-default@bundles/aspects"
	loader := NewLoader(nil, false, WithSeededBundles(map[string]*Bundle{canonical: b}))

	got, err := loader.Load(canonical)
	require.NoError(t, err)
	assert.Same(t, b, got, "the canonical ref resolves to the seeded bundle")

	infos, err := loader.List()
	require.NoError(t, err)
	var names []string
	for _, bi := range infos {
		names = append(names, bi.Name)
	}
	assert.Equal(t, []string{canonical}, names, "listed once under its canonical name")
}

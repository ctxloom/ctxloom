package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// TestLocalBundle_ItemRefIsBareAndLocal pins a project-local bundle's
// item ref: keyed on the bare, unprefixed name, resolving to IsLocal.
func TestLocalBundle_ItemRefIsBareAndLocal(t *testing.T) {
	seed := map[string]*bundles.Bundle{
		"dev": {
			Name: "dev",
			Fragments: map[string]bundles.BundleFragment{
				"keep": {
					ItemBody: bundles.ItemBody{
						Content: "KEEP-MARKER",
					},
				},
			},
		},
	}
	loader := seedLoader(t, seed)
	items, err := loader.ReadFragment("dev#fragments/keep")
	require.NoError(t, err)
	require.Len(t, items, 1)

	const wantRef = "ctxloom+local:dev#fragments/keep"
	assert.Equal(t, wantRef, items[0].ItemRef,
		"a project-local bundle's ItemRef must stay keyed on the bare, unprefixed name")

	tRef := mustParseProducerRef(t, items[0].ItemRef)
	assert.True(t, tRef.IsLocal, "a project-local item must still resolve to IsLocal")
	assert.Equal(t, "ctxloom:local", tRef.CanonicalURL())
}

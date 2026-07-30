package container

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEmbeddedAssets_ExportedGlobalsAreProcessWideMutable characterizes the
// CURRENT delivery shape of the three embedded assets: they are exported
// `[]byte` package globals, so a write through the slice any importer holds
// changes the bytes every LATER reader of that same asset sees, for the rest
// of the process. Nothing in the package restores them.
//
// Written before the accessor conversion so the collapse is provably a
// behaviour change in exactly one respect (isolation between readers) and in
// no other: the bytes each reader observes are otherwise identical.
func TestEmbeddedAssets_ExportedGlobalsAreProcessWideMutable(t *testing.T) {
	require.NotEmpty(t, Base)
	first := Base[0]
	t.Cleanup(func() { Base[0] = first })

	Base[0] = 'X'
	assert.Equal(t, byte('X'), Base[0],
		"a write through the exported global is visible to every later reader of the same asset")
}

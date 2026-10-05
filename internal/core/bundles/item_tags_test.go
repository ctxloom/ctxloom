package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestListAllFragments_TagsAreEachListedOnce: an item's tags are its bundle's
// plus its own, and a fragment commonly restates its bundle's. A listing that
// shows "testing, tdd, testing" reads as broken, so a tag appears once, in
// first-seen order (bundle tags first).
func TestListAllFragments_TagsAreEachListedOnce(t *testing.T) {
	tmpDir := t.TempDir()
	bundleYAML := `
version: "1.0"
tags: [testing, tdd]
fragments:
  frag:
    tags: [testing, mutation]
    content: c
`
	writeTree(t, afero.NewOsFs(), seedBundleRoot(t, tmpDir, paths.LayoutV2), "test", bundleYAML)

	infos := NewLoader(NewProjectReader(nil, []string{tmpDir})).ListAllFragments()

	require.Len(t, infos, 1)
	assert.Equal(t, []string{"testing", "tdd", "mutation"}, infos[0].Tags)
}

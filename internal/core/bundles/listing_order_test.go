package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// A listing is what tag selection (Catalog.ByTags) assembles context from,
// so its order is the context's order: it must be a function of the corpus,
// never of map iteration. Within one bundle, items list by name.
func TestCatalog_ListAll_ItemsListByNameWithinABundle(t *testing.T) {
	tmpDir := t.TempDir()
	bundleYAML := `
version: "1.0"
fragments:
  zulu: {content: z}
  alpha: {content: a}
  mike: {content: m}
  echo: {content: e}
  kilo: {content: k}
  bravo: {content: b}
  yankee: {content: y}
  charlie: {content: c}
commands:
  zulu: {content: z}
  alpha: {content: a}
  mike: {content: m}
  echo: {content: e}
  kilo: {content: k}
  bravo: {content: b}
  yankee: {content: y}
  charlie: {content: c}
`
	writeTree(t, afero.NewOsFs(), seedBundleRoot(t, tmpDir, paths.LayoutV2), "order", bundleYAML)
	cat := NewLoader(NewProjectReader(nil, []string{tmpDir})).Catalog()
	want := []string{"alpha", "bravo", "charlie", "echo", "kilo", "mike", "yankee", "zulu"}

	frags, err := cat.ListAllFragments()
	require.NoError(t, err)
	require.Equal(t, want, names(frags))

	cmds, err := cat.ListAllCommands()
	require.NoError(t, err)
	require.Equal(t, want, names(cmds))
}

func names(infos []ContentInfo) []string {
	out := make([]string, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}

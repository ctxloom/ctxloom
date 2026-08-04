package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListAllCommands_TagsDoNotAliasBundleTags is a regression guard:
// ListAllCommands previously built each prompt's tag list with
// append(bundle.Tags, prompt.Tags...), which writes into bundle.Tags' spare
// capacity — two prompts in one bundle would then share (and clobber) the same
// backing array. ListAllFragments already used slices.Concat; this pins the
// prompt side to the same non-aliasing behavior.
func TestListAllCommands_TagsDoNotAliasBundleTags(t *testing.T) {
	bundleTags := make([]string, 1, 4) // spare capacity makes aliasing observable
	bundleTags[0] = "shared"
	b := &Bundle{
		Version: "1.0.0",
		Tags:    bundleTags,
		Commands: map[string]BundleCommand{
			"alpha": {Content: "a", Tags: []string{"alpha-tag"}},
			"beta":  {Content: "b", Tags: []string{"beta-tag"}},
		},
	}
	loader := NewLoader(seedLocal(map[string]*Bundle{"seeded": b}))

	infos, err := loader.ListAllCommands()
	require.NoError(t, err)
	require.Len(t, infos, 2)

	byName := map[string][]string{}
	for _, info := range infos {
		byName[info.Name] = info.Tags
	}
	assert.Equal(t, []string{"shared", "alpha-tag"}, byName["alpha"])
	assert.Equal(t, []string{"shared", "beta-tag"}, byName["beta"])
	assert.Equal(t, []string{"shared"}, b.Tags, "bundle.Tags must not be written through")
}

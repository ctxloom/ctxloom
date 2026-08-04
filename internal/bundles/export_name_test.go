package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ExportName is the slash-command-facing name for a loaded item. Remote
// bundles are keyed by canonical ref ("<url>@bundles/<path>"); exporting that
// verbatim names the command after the entire URL (and ':' is invalid in
// filenames on Windows), so the export name shortens the bundle part to its
// last path segment. Name remains the full identity.
func TestLoadedContent_ExportName(t *testing.T) {
	tests := []struct {
		name     string
		content  LoadedContent
		expected string
	}{
		{
			name: "remote canonical bundle shortens to its last path segment",
			content: LoadedContent{
				Name:   "https://github.com/owner/personal@bundles/go-development/code-review",
				Bundle: "https://github.com/owner/personal@bundles/go-development",
				Item:   "code-review",
			},
			expected: "go-development/code-review",
		},
		{
			name: "nested remote bundle path keeps only the last segment",
			content: LoadedContent{
				Name:   "https://github.com/owner/repo@bundles/team/go-dev/review",
				Bundle: "https://github.com/owner/repo@bundles/team/go-dev",
				Item:   "review",
			},
			expected: "go-dev/review",
		},
		{
			name: "local bundle name is used as-is",
			content: LoadedContent{
				Name:   "go-development/review",
				Bundle: "go-development",
				Item:   "review",
			},
			expected: "go-development/review",
		},
		{
			name: "ctxloom-local ref shortens like a remote ref",
			content: LoadedContent{
				Name:   "ctxloom:local@bundles/foo/prompt",
				Bundle: "ctxloom:local@bundles/foo",
				Item:   "prompt",
			},
			expected: "foo/prompt",
		},
		{
			name: "builtin prompt without bundle metadata falls back to Name",
			content: LoadedContent{
				Name: "check-triggers",
			},
			expected: "check-triggers",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.content.ExportName())
		})
	}
}

// fragmentContent/commandContent must carry the owning bundle's loader name and
// the bare item name so export-name derivation doesn't have to re-parse the
// composite Name.
func TestLoader_LoadedContentCarriesBundleAndItem(t *testing.T) {
	const canonical = "https://github.com/ctxloom/ctxloom-default@bundles/aspects"
	b := &Bundle{
		Version:   "1.0.0",
		Fragments: map[string]BundleFragment{"security": {Content: "SEC"}},
		Commands:  map[string]BundleCommand{"review": {Content: "REVIEW"}},
	}
	loader := NewLoader(seedLocal(map[string]*Bundle{canonical: b}))

	infos, err := loader.ListAllCommands()
	require.NoError(t, err)
	require.Len(t, infos, 1)

	prompt, err := ungated(loader, false).GetCommand(infos[0].Name)
	require.NoError(t, err)
	assert.Equal(t, canonical, prompt.Bundle)
	assert.Equal(t, "review", prompt.Item)
	assert.Equal(t, "aspects/review", prompt.ExportName())

	frag, err := ungated(loader, false).GetFragment(canonical + "#fragments/security")
	require.NoError(t, err)
	assert.Equal(t, canonical, frag.Bundle)
	assert.Equal(t, "security", frag.Item)
}

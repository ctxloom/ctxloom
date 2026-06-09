package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// remotePrompt builds a LoadedContent the way the seeded bundle loader does:
// remote bundles are keyed by their canonical ref, so Name carries the full
// URL and Bundle/Item carry the parts.
func remotePrompt(bundleRef, item string) *bundles.LoadedContent {
	return &bundles.LoadedContent{
		Name:    bundleRef + "/" + item,
		Bundle:  bundleRef,
		Item:    item,
		Content: "body",
	}
}

// Slash-command exports must carry the SHORT export name, not the canonical
// URL: exporting the composite loader name verbatim produced commands named
// "/https:--github.com-..." (unusable, and ':' is invalid in filenames on
// Windows).
func TestExports_UseShortNamesForRemoteBundles(t *testing.T) {
	prompts := []*bundles.LoadedContent{
		remotePrompt("https://github.com/owner/personal@bundles/go-development", "code-review"),
	}

	claudeEx := claudeExports(prompts)
	require.Len(t, claudeEx, 1)
	assert.Equal(t, "go-development/code-review", claudeEx[0].Name)

	geminiEx := geminiExports(prompts)
	require.Len(t, geminiEx, 1)
	assert.Equal(t, "go-development/code-review", geminiEx[0].Name)
}

// When two bundles shorten to the same export name, both fall back to their
// full (sanitized) identity instead of silently overwriting each other's
// command file.
func TestExports_CollisionFallsBackToFullSanitizedName(t *testing.T) {
	prompts := []*bundles.LoadedContent{
		remotePrompt("https://github.com/alice/repo@bundles/go-dev", "review"),
		remotePrompt("https://github.com/bob/repo@bundles/go-dev", "review"),
	}

	claudeEx := claudeExports(prompts)
	require.Len(t, claudeEx, 2)
	assert.NotEqual(t, claudeEx[0].Name, claudeEx[1].Name, "colliding exports must stay distinct")
	for _, e := range claudeEx {
		assert.NotContains(t, e.Name, ":", "fallback names must be filesystem-safe on Windows")
	}
}

// Builtin prompts have no bundle metadata; their names pass through untouched.
func TestExports_BuiltinPromptNamePassesThrough(t *testing.T) {
	prompts := []*bundles.LoadedContent{{Name: "check-triggers", Content: "body"}}

	claudeEx := claudeExports(prompts)
	require.Len(t, claudeEx, 1)
	assert.Equal(t, "check-triggers", claudeEx[0].Name)
}

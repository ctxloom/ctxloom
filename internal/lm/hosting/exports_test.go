package hosting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
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

func enableAll(*bundles.LoadedContent) agent.CommandExport { return agent.CommandExport{Enabled: true} }

// Slash-command exports must carry the SHORT export name, not the canonical
// URL: exporting the composite loader name verbatim produced commands named
// "/https:--github.com-..." (unusable, and ':' is invalid in filenames on
// Windows).
func TestBuildCommandExports_UseShortNamesForRemoteBundles(t *testing.T) {
	prompts := []*bundles.LoadedContent{
		remotePrompt("https://github.com/owner/personal@bundles/go-development", "code-review"),
	}
	ex := BuildCommandExports(prompts, enableAll)
	require.Len(t, ex, 1)
	assert.Equal(t, "go-development/code-review", ex[0].Name)
	assert.Equal(t, "body", ex[0].Content)
}

// When two bundles shorten to the same export name, both fall back to their
// full (sanitized) identity instead of silently overwriting each other's
// command file.
func TestBuildCommandExports_CollisionFallsBackToFullSanitizedName(t *testing.T) {
	prompts := []*bundles.LoadedContent{
		remotePrompt("https://github.com/alice/repo@bundles/go-dev", "review"),
		remotePrompt("https://github.com/bob/repo@bundles/go-dev", "review"),
	}
	ex := BuildCommandExports(prompts, enableAll)
	require.Len(t, ex, 2)
	assert.NotEqual(t, ex[0].Name, ex[1].Name, "colliding exports must stay distinct")
	for _, e := range ex {
		assert.NotContains(t, e.Name, ":", "fallback names must be filesystem-safe on Windows")
	}
}

// Builtin prompts have no bundle metadata; their names pass through untouched.
func TestBuildCommandExports_BuiltinPromptNamePassesThrough(t *testing.T) {
	prompts := []*bundles.LoadedContent{{Name: "check-triggers", Content: "body"}}
	ex := BuildCommandExports(prompts, enableAll)
	require.Len(t, ex, 1)
	assert.Equal(t, "check-triggers", ex[0].Name)
}

// The pick's enablement and the skill's own frontmatter both reach the
// export; the file mode crosses as the POSIX bits the loader recorded.
func TestBuildSkillExports_CarriesFrontmatterFilesAndPick(t *testing.T) {
	skills := []*bundles.LoadedSkill{{
		Frontmatter: bundles.SkillFrontmatter{Name: "s", Description: "does s"},
		Files:       []bundles.LoadedSkillFile{{RelPath: "run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0o755}},
	}}
	ex := BuildSkillExports(skills, func(*bundles.LoadedSkill) bool { return false })
	require.Len(t, ex, 1)
	assert.Equal(t, "s", ex[0].Name)
	assert.Equal(t, "does s", ex[0].Description)
	assert.False(t, ex[0].Enabled)
	require.Len(t, ex[0].Files, 1)
	assert.Equal(t, "run.sh", ex[0].Files[0].RelPath)
	assert.EqualValues(t, 0o755, ex[0].Files[0].Mode)
}

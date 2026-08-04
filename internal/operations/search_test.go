// Search operation tests verify that users can find content across all ctxloom types
// (fragments, prompts, profiles, MCP servers). Search is exposed via MCP tools
// to help users discover and use available context.
package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// =============================================================================
// Search Result Structure Tests
// =============================================================================
// SearchResult captures match details for display and filtering.

func TestSearchResult_Fields(t *testing.T) {
	result := SearchResult{
		Type:   "fragment",
		Name:   "my-fragment",
		Tags:   []string{"go", "testing"},
		Source: "local",
		Match:  "name",
	}

	assert.Equal(t, "fragment", result.Type)
	assert.Equal(t, "my-fragment", result.Name)
	assert.Equal(t, []string{"go", "testing"}, result.Tags)
	assert.Equal(t, "local", result.Source)
	assert.Equal(t, "name", result.Match)
}

func TestSearchContentRequest_Defaults(t *testing.T) {
	// Zero-value request uses sensible defaults
	req := SearchContentRequest{}

	assert.Empty(t, req.Query)
	assert.Nil(t, req.Types)
	assert.Nil(t, req.Tags)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
	assert.Zero(t, req.Limit)
}

func TestSearchContentRequest_AllTypes(t *testing.T) {
	req := SearchContentRequest{
		Query: "test",
		Types: []string{"fragment", "command", "profile", "mcp_server"},
		Limit: 100,
	}

	assert.Equal(t, "test", req.Query)
	assert.Len(t, req.Types, 4)
	assert.Equal(t, 100, req.Limit)
}

func TestSearchContentResult_Fields(t *testing.T) {
	result := SearchContentResult{
		Results: []SearchResult{
			{Type: "fragment", Name: "frag1", Match: "name"},
			{Type: "command", Name: "prompt1", Match: "name"},
		},
		Count: 2,
		Query: "test",
	}

	assert.Len(t, result.Results, 2)
	assert.Equal(t, 2, result.Count)
	assert.Equal(t, "test", result.Query)
}

func TestSearchContentResult_Empty(t *testing.T) {
	result := SearchContentResult{
		Results: []SearchResult{},
		Count:   0,
		Query:   "nonexistent",
	}

	assert.Empty(t, result.Results)
	assert.Zero(t, result.Count)
}

func TestSearchResult_MatchTypes(t *testing.T) {
	tests := []struct {
		name      string
		matchType string
		valid     bool
	}{
		{"name match", "name", true},
		{"tag match", "tag", true},
		{"description match", "description", true},
		{"empty match", "", true}, // Empty is valid for no match info
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SearchResult{
				Type:  "fragment",
				Name:  "test",
				Match: tt.matchType,
			}
			if tt.valid {
				assert.NotNil(t, result)
			}
		})
	}
}

func TestSearchContentRequest_TypeFiltering(t *testing.T) {
	tests := []struct {
		name     string
		types    []string
		expected int
	}{
		{
			name:     "single type",
			types:    []string{"fragment"},
			expected: 1,
		},
		{
			name:     "multiple types",
			types:    []string{"fragment", "profile"},
			expected: 2,
		},
		{
			name:     "all types",
			types:    []string{"fragment", "command", "profile", "mcp_server"},
			expected: 4,
		},
		{
			name:     "empty types searches all",
			types:    []string{},
			expected: 0, // Empty means search all
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := SearchContentRequest{
				Query: "test",
				Types: tt.types,
			}
			assert.Len(t, req.Types, tt.expected)
		})
	}
}

func TestSearchContentRequest_Sorting(t *testing.T) {
	tests := []struct {
		name      string
		sortBy    string
		sortOrder string
	}{
		{"sort by name ascending", "name", "asc"},
		{"sort by name descending", "name", "desc"},
		{"sort by type ascending", "type", "asc"},
		{"sort by type descending", "type", "desc"},
		{"sort by relevance ascending", "relevance", "asc"},
		{"sort by relevance descending", "relevance", "desc"},
		{"default sorting", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := SearchContentRequest{
				Query:     "test",
				SortBy:    tt.sortBy,
				SortOrder: tt.sortOrder,
			}
			assert.NotNil(t, req)
		})
	}
}

// =============================================================================
// Search Integration Tests
// =============================================================================
// Integration tests verify search across real bundle structures.

func setupSearchTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	// Create a test bundle with fragments and prompts
	bundleContent := `version: "1.0"
description: Test bundle for search
fragments:
  security-practices:
    tags: ["security", "best-practices"]
    content: |
      Security best practices for development
  golang-testing:
    tags: ["go", "testing", "tdd"]
    content: |
      Go testing guidelines
  react-components:
    tags: ["react", "frontend", "javascript"]
    content: |
      React component patterns
commands:
  code-review:
    description: Review code for issues
    content: |
      Please review this code
  refactor-code:
    description: Refactor code for clarity
    content: |
      Refactor this code
`
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev-tools.yaml", []byte(bundleContent), 0644)

	loader := bundles.NewLoader(bundles.NewProjectReader(fs, []string{paths.LocalBundlesPath(testBaseDir)}))
	return fs, loader
}

func TestSearchContent_ValidationError(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	_, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "query or tags required")
}

func TestSearchContent_SearchFragmentsByName(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "security",
		Types:  []string{"fragment"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, r := range result.Results {
		if r.Type == "fragment" && r.Name == "security-practices" {
			found = true
			assert.Equal(t, "name", r.Match)
			break
		}
	}
	assert.True(t, found, "should find security-practices fragment")
}

func TestSearchContent_FindsSeededRemoteBundleFragments(t *testing.T) {
	// Regression: remote-resolved bundles are seeded into the loader from the
	// active lockfile by SeededBundleLoader — they don't live on disk as
	// extracted YAML. search_content must index those seeded bundles, not just
	// locally-authored ones. This closes the loop on the trust_remote fix:
	// once a remote bundle is promoted into the active lockfile and seeded,
	// its fragments must surface in search (previously they were invisible
	// because the bundle was stranded in pending and never seeded).
	const remoteName = "acme/security"
	b, err := bundles.ParseBundle([]byte(`version: "1.0"
description: Remote security bundle
fragments:
  threat-modeling:
    tags: ["security", "remote"]
    content: |
      Threat modeling guidance
`))
	require.NoError(t, err)
	b.Name = remoteName

	// A loader with NO local bundle dirs, seeded only with the remote bundle —
	// mirrors SeededBundleLoader resolving a remote bundle from the lockfile
	// when nothing is authored locally.
	loader := bundles.NewLoader(nil,
		bundles.WithFS(afero.NewMemMapFs()),
		bundles.WithSeededBundles(map[string]*bundles.Bundle{remoteName: b}),
	)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "threat-modeling",
		Types:  []string{"fragment"},
		Loader: loader,
	})
	require.NoError(t, err)

	found := false
	for _, r := range result.Results {
		if r.Type == "fragment" && r.Name == "threat-modeling" {
			found = true
			assert.Equal(t, remoteName, r.Source, "fragment must be attributed to the remote bundle")
			break
		}
	}
	assert.True(t, found, "fragment from a seeded remote bundle must be searchable")
}

func TestSearchContent_SearchFragmentsByTag(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "tdd",
		Types:  []string{"fragment"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, r := range result.Results {
		if r.Type == "fragment" && r.Name == "golang-testing" {
			found = true
			assert.Equal(t, "tag", r.Match)
			break
		}
	}
	assert.True(t, found, "should find golang-testing fragment by tag")
}

func TestSearchContent_TagsOnlyQueryIsFragmentScoped(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-developer": {Description: "Go development profile", Tags: []string{"go"}},
		}},
		MCP: wire.MCPConfig{Servers: map[string]wire.MCPServer{
			"filesystem": {Command: "npx"},
		}},
	})

	// Tags-only search (empty query): tags filter fragments only. An empty query
	// must NOT flood the results with every prompt, profile, and mcp_server
	// (strings.Contains(s, "") is unconditionally true) — only tag-matched
	// fragments should come back.
	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Tags:   []string{"go"},
		Loader: loader,
	})
	require.NoError(t, err)

	sawFragment := false
	for _, r := range result.Results {
		assert.NotEqual(t, "prompt", r.Type, "tags-only search must not return prompts")
		assert.NotEqual(t, "profile", r.Type, "tags-only search must not return profiles")
		assert.NotEqual(t, "mcp_server", r.Type, "tags-only search must not return mcp servers")
		if r.Type == "fragment" && r.Name == "golang-testing" {
			sawFragment = true
		}
	}
	assert.True(t, sawFragment, "tags-only search should still return the tag-matched fragment")
}

func TestSearchContent_SearchPrompts(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "review",
		Types:  []string{"command"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, r := range result.Results {
		if r.Type == "command" && r.Match == "name" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find prompt by name")
}

// TestSearchContent_SearchSkills proves SearchContent indexes Agent Skill
// packages (a directory-form bundle's skills/<name>/ tree), not just
// single-blob fragments/commands — the search operation's counterpart to
// ListAllSkills, exercised end to end through SearchContent{Types: []string{"skill"}}
// the way `ctxloom search --type skill` drives it.
func TestSearchContent_SearchSkills(t *testing.T) {
	fsys := afero.NewMemMapFs()
	bundlesDir := paths.LocalBundlesPath(testBaseDir)
	bundleDir := bundlesDir + "/skill-bundle"
	require.NoError(t, fsys.MkdirAll(bundleDir+"/skills/humanize", 0755))
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/bundle.yaml",
		[]byte("name: skill-bundle\nversion: \"1.0\"\nskills:\n  humanize:\n"), 0644))
	require.NoError(t, afero.WriteFile(fsys, bundleDir+"/skills/humanize/SKILL.md",
		[]byte("---\nname: humanize\ndescription: Rewrites text to sound less like an AI wrote it.\n---\n\n# humanize\n\nBody.\n"), 0644))

	loader := bundles.NewLoader(bundles.NewProjectReader(fsys, []string{bundlesDir}))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "humanize",
		Types:  []string{"skill"},
		Loader: loader,
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Count)
	got := result.Results[0]
	assert.Equal(t, "skill", got.Type)
	assert.Equal(t, "skill-bundle/humanize", got.Name)
	assert.Equal(t, "name", got.Match)

	// A query that only hits the SKILL.md description still resolves — proves
	// the description path (not just the name path) is wired.
	result, err = SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "sound less like an ai",
		Types:  []string{"skill"},
		Loader: loader,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Count)
	assert.Equal(t, "description", result.Results[0].Match)
}

func TestSearchContent_SearchProfiles(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"go-developer": {
				Description: "Go development profile",
				Tags:        []string{"go", "backend"},
			},
			"frontend-dev": {
				Description: "Frontend development",
				Tags:        []string{"react", "javascript"},
			},
		}},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "go",
		Types: []string{"profile"},
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, r := range result.Results {
		if r.Type == "profile" && r.Name == "go-developer" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find go-developer profile")
}

func TestSearchContent_SearchMCPServers(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		MCP: wire.MCPConfig{
			Servers: map[string]wire.MCPServer{
				"filesystem": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-filesystem"},
				},
				"github": {
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-github"},
				},
			},
		},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "github",
		Types: []string{"mcp_server"},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "mcp_server", result.Results[0].Type)
	assert.Equal(t, "github", result.Results[0].Name)
}

func TestSearchContent_MultipleTypes(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"react-developer": {
				Description: "React development profile",
				Tags:        []string{"react", "frontend"},
			},
		}},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "react",
		Types:  []string{"fragment", "profile"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 2) // Should find fragment and profile

	hasFragment := false
	hasProfile := false
	for _, r := range result.Results {
		if r.Type == "fragment" {
			hasFragment = true
		}
		if r.Type == "profile" {
			hasProfile = true
		}
	}
	assert.True(t, hasFragment, "should find fragments")
	assert.True(t, hasProfile, "should find profiles")
}

func TestSearchContent_SortByName(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:     "ing", // matches "testing" and other fragments
		Types:     []string{"fragment"},
		SortBy:    "name",
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	if len(result.Results) >= 2 {
		// Verify sorted ascending
		for i := 1; i < len(result.Results); i++ {
			assert.LessOrEqual(t, result.Results[i-1].Name, result.Results[i].Name)
		}
	}
}

func TestSearchContent_SortByRelevance(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:     "go",
		Types:     []string{"fragment"},
		SortBy:    "relevance",
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	// Name matches should come first in relevance sort
	if len(result.Results) > 0 {
		// First results should have name or tag matches
		assert.Contains(t, []string{"name", "tag"}, result.Results[0].Match)
	}
}

func TestSearchContent_WithLimit(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "a", // Should match many items
		Types:  []string{"fragment", "command"},
		Limit:  2,
		Loader: loader,
	})

	require.NoError(t, err)
	assert.LessOrEqual(t, result.Count, 2)
	// TotalMatches is the pre-truncation count: callers (the CLI's search
	// command) subtract Count from it to report how many matches the limit
	// hid, so a truncated answer never silently looks like the whole answer.
	assert.GreaterOrEqual(t, result.TotalMatches, result.Count)
	if result.Count == 2 {
		assert.Greater(t, result.TotalMatches, result.Count, "a 2-item cap on a query matching many items should truncate")
	}
}

// TestSearchContent_TotalMatchesEqualsCountWhenUnderLimit pins the other half
// of the truncation contract: when nothing was cut, TotalMatches and Count
// must agree exactly (no phantom "hidden" matches reported for an answer
// that was already complete).
func TestSearchContent_TotalMatchesEqualsCountWhenUnderLimit(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "a",
		Types:  []string{"fragment", "command"},
		Limit:  1000,
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, result.Count, result.TotalMatches)
}

func TestSearchContent_DefaultLimit(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "a",
		Types:  []string{"fragment"},
		Loader: loader,
		// Limit not set, should default to 50
	})

	require.NoError(t, err)
	assert.LessOrEqual(t, result.Count, 50)
}

func TestSearchContent_SearchAllTypesWhenEmpty(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test-profile": {Description: "Test"},
		}},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "test",
		Types:  []string{}, // Empty means search all
		Loader: loader,
	})

	require.NoError(t, err)
	// Should search across all types
	assert.GreaterOrEqual(t, result.Count, 1)
}

func TestSearchContent_SortByType(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test-profile": {Description: "Test profile"},
		}},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "test",
		Types:  []string{"fragment", "profile"},
		SortBy: "type",
		Loader: loader,
	})

	require.NoError(t, err)
	// Results should be sorted by type
	if len(result.Results) >= 2 {
		for i := 1; i < len(result.Results); i++ {
			assert.LessOrEqual(t, result.Results[i-1].Type, result.Results[i].Type)
		}
	}
}

func TestSearchContent_SortDescending(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:     "ing", // matches "testing"
		Types:     []string{"fragment"},
		SortBy:    "name",
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	if len(result.Results) >= 2 {
		// Verify sorted descending
		for i := 1; i < len(result.Results); i++ {
			assert.GreaterOrEqual(t, result.Results[i-1].Name, result.Results[i].Name)
		}
	}
}

func TestSearchContent_ProfileByDescription(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{testBaseDir},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"my-profile": {
				Description: "This is a unique description for searching",
				Tags:        []string{"go"},
			},
		}},
	})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "unique description",
		Types: []string{"profile"},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "my-profile", result.Results[0].Name)
	assert.Equal(t, "description", result.Results[0].Match)
}

// =============================================================================
// Local Search Tests
// =============================================================================
// SearchContent searches local content (fragments, prompts, profiles,
// mcp_servers) only — remote discovery is search_library's job (the
// unreachable SearchRemote branch and its SearchLocal/SearchRemote
// scope-flag fields were deleted; the CLI's own concurrent local+remote fan-out in
// cli/search.go never routed through them anyway).

func TestSearchContent_LocalScope(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "security",
		Types:  []string{"fragment"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)
	// All results should be local (fragment type)
	for _, r := range result.Results {
		assert.Equal(t, "fragment", r.Type)
	}
}

func TestSearchContent_BundleType(t *testing.T) {
	// Bundle type is remote-only content
	// When searching for bundles, should search configured remotes
	req := SearchContentRequest{
		Query: "golang",
		Types: []string{"bundle"},
	}

	assert.Contains(t, req.Types, "bundle")
}

func TestSearchResult_RemoteSource(t *testing.T) {
	// Remote search results should include remote name as source
	result := SearchResult{
		Type:   "bundle",
		Name:   "go-practices",
		Tags:   []string{"go", "best-practices"},
		Source: "personal", // Remote name
		Match:  "name",
	}

	assert.Equal(t, "bundle", result.Type)
	assert.Equal(t, "personal", result.Source)
}

// TestSortResults_RelevanceTiesAreDeterministic is a regression guard:
// sortResults used sort.Slice (unstable) and relevanceScore has
// only 3 distinct values, so a set of results tied on relevance had NO
// deterministic secondary order — whatever order the (randomized)
// map-iteration producers (searchProfiles, searchMCPServers) happened to
// hand it stuck, and a subsequent Limit truncation could then keep a
// different arbitrary subset of an otherwise-identical answer on
// consecutive runs of the same query. Two different input orderings of the
// same tied-relevance set must sort to the SAME output order.
func TestSortResults_RelevanceTiesAreDeterministic(t *testing.T) {
	a := SearchResult{Type: "fragment", Name: "bravo", Match: "name"}
	b := SearchResult{Type: "fragment", Name: "alpha", Match: "name"}
	c := SearchResult{Type: "fragment", Name: "charlie", Match: "name"}

	order1 := []SearchResult{a, b, c}
	order2 := []SearchResult{c, a, b}

	sortResults(order1, "relevance", false)
	sortResults(order2, "relevance", false)

	names := func(rs []SearchResult) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Name
		}
		return out
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, names(order1))
	assert.Equal(t, names(order1), names(order2),
		"two different input orderings of the same tied-relevance set must sort identically")
}

// TestSortResults_TypeTiesAreDeterministic mirrors the relevance case for
// the "type" sort, which ties even more often
// (every same-type result ties).
func TestSortResults_TypeTiesAreDeterministic(t *testing.T) {
	a := SearchResult{Type: "fragment", Name: "bravo"}
	b := SearchResult{Type: "fragment", Name: "alpha"}
	c := SearchResult{Type: "fragment", Name: "charlie"}

	order1 := []SearchResult{a, b, c}
	order2 := []SearchResult{c, a, b}

	sortResults(order1, "type", false)
	sortResults(order2, "type", false)

	names := func(rs []SearchResult) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Name
		}
		return out
	}
	assert.Equal(t, []string{"alpha", "bravo", "charlie"}, names(order1))
	assert.Equal(t, names(order1), names(order2),
		"two different input orderings of the same tied-type set must sort identically")
}

func TestSearchContent_NoScopeFlagsNeeded(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "test",
		Types:  []string{"fragment"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.NotNil(t, result)
}

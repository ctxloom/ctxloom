// Search operation tests verify that users can find content across all SCM types
// (fragments, prompts, profiles, MCP servers). Search is exposed via MCP tools
// to help users discover and use available context.
package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benjaminabbitt/scm/internal/bundles"
	"github.com/benjaminabbitt/scm/internal/config"
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
		Types: []string{"fragment", "prompt", "profile", "mcp_server"},
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
			{Type: "prompt", Name: "prompt1", Match: "name"},
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
			types:    []string{"fragment", "prompt", "profile", "mcp_server"},
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
	_ = fs.MkdirAll("/project/.scm/bundles", 0755)

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
prompts:
  code-review:
    description: Review code for issues
    content: |
      Please review this code
  refactor-code:
    description: Refactor code for clarity
    content: |
      Refactor this code
`
	_ = afero.WriteFile(fs, "/project/.scm/bundles/dev-tools.yaml", []byte(bundleContent), 0644)

	loader := bundles.NewLoader([]string{"/project/.scm/bundles"}, false, bundles.WithFS(fs))
	return fs, loader
}

func TestSearchContent_ValidationError(t *testing.T) {
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	_, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "query is required")
}

func TestSearchContent_SearchFragmentsByName(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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

func TestSearchContent_SearchFragmentsByTag(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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

func TestSearchContent_SearchPrompts(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "review",
		Types:  []string{"prompt"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, r := range result.Results {
		if r.Type == "prompt" && r.Match == "name" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find prompt by name")
}

func TestSearchContent_SearchProfiles(t *testing.T) {
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"go-developer": {
				Description: "Go development profile",
				Tags:        []string{"go", "backend"},
			},
			"frontend-dev": {
				Description: "Frontend development",
				Tags:        []string{"react", "javascript"},
			},
		},
	}

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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		MCP: config.MCPConfig{
			Servers: map[string]config.MCPServer{
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
	}

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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"react-developer": {
				Description: "React development profile",
				Tags:        []string{"react", "frontend"},
			},
		},
	}

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
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query:  "a", // Should match many items
		Types:  []string{"fragment", "prompt"},
		Limit:  2,
		Loader: loader,
	})

	require.NoError(t, err)
	assert.LessOrEqual(t, result.Count, 2)
}

func TestSearchContent_DefaultLimit(t *testing.T) {
	_, loader := setupSearchTestFS(t)
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"test-profile": {Description: "Test"},
		},
	}

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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"test-profile": {Description: "Test profile"},
		},
	}

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
	cfg := &config.Config{SCMPaths: []string{"/project/.scm"}}

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
	cfg := &config.Config{
		SCMPaths: []string{"/project/.scm"},
		Profiles: map[string]config.Profile{
			"my-profile": {
				Description: "This is a unique description for searching",
				Tags:        []string{"go"},
			},
		},
	}

	result, err := SearchContent(context.Background(), cfg, SearchContentRequest{
		Query: "unique description",
		Types: []string{"profile"},
	})

	require.NoError(t, err)
	assert.Equal(t, 1, result.Count)
	assert.Equal(t, "my-profile", result.Results[0].Name)
	assert.Equal(t, "description", result.Results[0].Match)
}

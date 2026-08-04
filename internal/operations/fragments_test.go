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
)

func TestGetFragment_ValidationError(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	_, err := GetFragment(context.Background(), cfg, GetFragmentRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestContainsTag(t *testing.T) {
	tests := []struct {
		name     string
		tags     []string
		query    string
		expected bool
	}{
		{
			name:     "exact match",
			tags:     []string{"go", "testing"},
			query:    "go",
			expected: true,
		},
		{
			name:     "partial match",
			tags:     []string{"golang", "testing"},
			query:    "go",
			expected: true,
		},
		{
			name:     "case insensitive",
			tags:     []string{"Go", "Testing"},
			query:    "go",
			expected: true,
		},
		{
			name:     "no match",
			tags:     []string{"python", "testing"},
			query:    "go",
			expected: false,
		},
		{
			name:     "empty tags",
			tags:     []string{},
			query:    "go",
			expected: false,
		},
		{
			name:     "empty query",
			tags:     []string{"go"},
			query:    "",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := containsTag(tt.tags, tt.query)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSortContentInfos(t *testing.T) {
	tests := []struct {
		name      string
		infos     []bundles.ContentInfo
		sortBy    string
		sortOrder string
		expected  []string // Expected names in order
	}{
		{
			name: "sort by name ascending",
			infos: []bundles.ContentInfo{
				{Name: "zebra"},
				{Name: "apple"},
				{Name: "mango"},
			},
			sortBy:    "name",
			sortOrder: "asc",
			expected:  []string{"apple", "mango", "zebra"},
		},
		{
			name: "sort by name descending",
			infos: []bundles.ContentInfo{
				{Name: "zebra"},
				{Name: "apple"},
				{Name: "mango"},
			},
			sortBy:    "name",
			sortOrder: "desc",
			expected:  []string{"zebra", "mango", "apple"},
		},
		{
			name: "sort by source ascending",
			infos: []bundles.ContentInfo{
				{Name: "a", Source: "z-source"},
				{Name: "b", Source: "a-source"},
				{Name: "c", Source: "m-source"},
			},
			sortBy:    "source",
			sortOrder: "asc",
			expected:  []string{"b", "c", "a"},
		},
		{
			name: "default sort is name ascending",
			infos: []bundles.ContentInfo{
				{Name: "charlie"},
				{Name: "alpha"},
				{Name: "bravo"},
			},
			sortBy:    "",
			sortOrder: "",
			expected:  []string{"alpha", "bravo", "charlie"},
		},
		{
			name: "case insensitive name sort",
			infos: []bundles.ContentInfo{
				{Name: "Zebra"},
				{Name: "apple"},
				{Name: "MANGO"},
			},
			sortBy:    "name",
			sortOrder: "asc",
			expected:  []string{"apple", "MANGO", "Zebra"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sortContentInfos(tt.infos, tt.sortBy, tt.sortOrder)
			names := make([]string, len(tt.infos))
			for i, info := range tt.infos {
				names[i] = info.Name
			}
			assert.Equal(t, tt.expected, names)
		})
	}
}

func TestFragmentEntry_Fields(t *testing.T) {
	entry := FragmentEntry{
		Name:   "test-fragment",
		Tags:   []string{"tag1", "tag2"},
		Source: "local",
	}

	assert.Equal(t, "test-fragment", entry.Name)
	assert.Equal(t, []string{"tag1", "tag2"}, entry.Tags)
	assert.Equal(t, "local", entry.Source)
}

func TestListFragmentsRequest_Defaults(t *testing.T) {
	req := ListFragmentsRequest{}

	assert.Empty(t, req.Query)
	assert.Nil(t, req.Tags)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListFragmentsResult_Fields(t *testing.T) {
	result := ListFragmentsResult{
		Fragments: []FragmentEntry{
			{Name: "frag1", Tags: []string{"a"}, Source: "local"},
			{Name: "frag2", Tags: []string{"b"}, Source: "remote"},
		},
		Count: 2,
	}

	assert.Len(t, result.Fragments, 2)
	assert.Equal(t, 2, result.Count)
}

// ========== Loader-based integration tests ==========

func setupBundleTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	// Create a test bundle with fragments and prompts
	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  security:
    tags: ["security", "best-practices"]
    content: |
      Security best practices for development
  testing:
    tags: ["testing", "tdd"]
    content: |
      Test-driven development guidelines
  golang:
    tags: ["go", "best-practices"]
    content: |
      Go development best practices
commands:
  code-review:
    description: Review code
    content: |
      # Code Review
      Please review this code
  refactor:
    description: Refactor code
    content: |
      # Refactoring
      Refactor this code for clarity
`
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/test-bundle.yaml", []byte(bundleContent), 0644)

	// Create another bundle
	anotherBundle := `version: "1.0"
fragments:
  python:
    tags: ["python", "scripting"]
    content: Python development tips
`
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/another.yaml", []byte(anotherBundle), 0644)

	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))
	return fs, loader
}

func TestListFragments_AllFragments(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	result, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, 4, result.Count) // security, testing, golang, python
	assert.Len(t, result.Fragments, 4)
}

func TestListFragments_WithQuery(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	result, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		Query:  "go",
		Loader: loader,
	})

	require.NoError(t, err)
	// Should match "golang" by name
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, f := range result.Fragments {
		if f.Name == "golang" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find golang fragment")
}

func TestListFragments_WithTags(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	result, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		Tags:   []string{"security"},
		Loader: loader,
	})

	require.NoError(t, err)
	assert.GreaterOrEqual(t, result.Count, 1)

	// All results should have security tag
	for _, f := range result.Fragments {
		assert.Contains(t, f.Tags, "security")
	}
}

func TestListFragments_SortByName(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	result, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		SortBy:    "name",
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Fragments), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Fragments); i++ {
		assert.LessOrEqual(t, result.Fragments[i-1].Name, result.Fragments[i].Name)
	}
}

func TestListFragments_SortDescending(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	result, err := ListFragments(context.Background(), nil, ListFragmentsRequest{
		SortBy:    "name",
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Fragments), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Fragments); i++ {
		assert.GreaterOrEqual(t, result.Fragments[i-1].Name, result.Fragments[i].Name)
	}
}

func TestGetFragment_Success(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	// Use bundle#fragments/name syntax
	result, err := GetFragment(context.Background(), nil, GetFragmentRequest{
		Name:     "test-bundle#fragments/security",
		Pipeline: opPipe(nil, loader),
	})

	require.NoError(t, err)
	assert.Contains(t, result.Name, "security")
	assert.Contains(t, result.Content, "Security best practices")
	assert.Contains(t, result.Tags, "security")
}

func TestGetFragment_NotFound(t *testing.T) {
	_, loader := setupBundleTestFS(t)

	_, err := GetFragment(context.Background(), nil, GetFragmentRequest{
		Name:     "nonexistent-bundle#fragments/nope",
		Pipeline: opPipe(nil, loader),
	})

	require.Error(t, err)
}

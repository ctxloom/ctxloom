package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestSkillEntry_Fields(t *testing.T) {
	entry := SkillEntry{
		Name:   "my-prompt",
		Source: "local",
	}

	assert.Equal(t, "my-prompt", entry.Name)
	assert.Equal(t, "local", entry.Source)
}

func TestListSkillsRequest_Defaults(t *testing.T) {
	req := ListSkillsRequest{}

	assert.Empty(t, req.Query)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListSkillsResult_Fields(t *testing.T) {
	result := ListSkillsResult{
		Skills: []SkillEntry{
			{Name: "prompt1", Source: "local"},
			{Name: "prompt2", Source: "bundle"},
		},
		Count: 2,
	}

	assert.Len(t, result.Skills, 2)
	assert.Equal(t, 2, result.Count)
}

func TestGetSkillRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         GetSkillRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         GetSkillRequest{Name: "my-prompt"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         GetSkillRequest{Name: ""},
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.shouldError {
				assert.Empty(t, tt.req.Name)
			} else {
				assert.NotEmpty(t, tt.req.Name)
			}
		})
	}
}

func TestGetSkillResult_Fields(t *testing.T) {
	result := GetSkillResult{
		Name:    "code-review",
		Content: "Review this code:\n{{file}}",
	}

	assert.Equal(t, "code-review", result.Name)
	assert.Contains(t, result.Content, "Review this code")
}

// ========== Loader-based integration tests ==========

func setupPromptTestFS(t *testing.T) (afero.Fs, *bundles.Loader) {
	t.Helper()
	fs := afero.NewMemMapFs()

	// Create bundles directory
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)

	// Create a test bundle with prompts
	bundleContent := `version: "1.0"
description: Test bundle with prompts
skills:
  code-review:
    description: Review code for issues
    content: |
      # Code Review
      Please review the following code for:
      - Bugs
      - Security issues
      - Performance
  refactor:
    description: Refactor code
    content: |
      # Refactoring Request
      Refactor this code for better readability
  commit:
    description: Generate commit message
    content: |
      Generate a commit message for the staged changes
`
	_ = afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/dev-tools.yaml", []byte(bundleContent), 0644)

	// Create another bundle with more prompts
	anotherBundle := `version: "1.0"
skills:
  explain:
    description: Explain code
    content: |
      Explain what this code does
`
	_ = afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/learning.yaml", []byte(anotherBundle), 0644)

	loader := bundles.NewLoader([]string{paths.BundlesPath(testBaseDir)}, false, bundles.WithFS(fs))
	return fs, loader
}

func TestListSkills_AllPrompts(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListSkills(context.Background(), nil, ListSkillsRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, 4, result.Count) // code-review, refactor, commit, explain
	assert.Len(t, result.Skills, 4)
}

func TestListSkills_WithQuery(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListSkills(context.Background(), nil, ListSkillsRequest{
		Query:  "code",
		Loader: loader,
	})

	require.NoError(t, err)
	// Should match "code-review" by name
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, p := range result.Skills {
		if strings.Contains(p.Name, "code-review") {
			found = true
			break
		}
	}
	assert.True(t, found, "should find code-review prompt")
}

func TestListSkills_SortAscending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListSkills(context.Background(), nil, ListSkillsRequest{
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Skills), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Skills); i++ {
		assert.LessOrEqual(t, result.Skills[i-1].Name, result.Skills[i].Name)
	}
}

func TestListSkills_SortDescending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListSkills(context.Background(), nil, ListSkillsRequest{
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Skills), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Skills); i++ {
		assert.GreaterOrEqual(t, result.Skills[i-1].Name, result.Skills[i].Name)
	}
}

func TestGetSkill_Success(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	// Use bundle#skills/name syntax
	result, err := GetSkill(context.Background(), nil, GetSkillRequest{
		Name:   "dev-tools#skills/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Contains(t, result.Name, "code-review")
	assert.Contains(t, result.Content, "review")
}

func TestGetSkill_ValidationError(t *testing.T) {
	_, err := GetSkill(context.Background(), nil, GetSkillRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestGetSkill_NotFound(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	_, err := GetSkill(context.Background(), nil, GetSkillRequest{
		Name:   "nonexistent#skills/nope",
		Loader: loader,
	})

	require.Error(t, err)
}

func TestGetSkill_StripsHeaderLines(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := GetSkill(context.Background(), nil, GetSkillRequest{
		Name:   "dev-tools#skills/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	// The content should not start with # header after stripping
	assert.False(t, len(result.Content) > 0 && result.Content[0] == '#',
		"content should have header lines stripped")
}

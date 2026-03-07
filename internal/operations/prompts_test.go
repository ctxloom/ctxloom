package operations

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/benjaminabbitt/scm/internal/bundles"
)

func TestPromptEntry_Fields(t *testing.T) {
	entry := PromptEntry{
		Name:   "my-prompt",
		Source: "local",
	}

	assert.Equal(t, "my-prompt", entry.Name)
	assert.Equal(t, "local", entry.Source)
}

func TestListPromptsRequest_Defaults(t *testing.T) {
	req := ListPromptsRequest{}

	assert.Empty(t, req.Query)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListPromptsResult_Fields(t *testing.T) {
	result := ListPromptsResult{
		Prompts: []PromptEntry{
			{Name: "prompt1", Source: "local"},
			{Name: "prompt2", Source: "bundle"},
		},
		Count: 2,
	}

	assert.Len(t, result.Prompts, 2)
	assert.Equal(t, 2, result.Count)
}

func TestGetPromptRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         GetPromptRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         GetPromptRequest{Name: "my-prompt"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         GetPromptRequest{Name: ""},
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

func TestGetPromptResult_Fields(t *testing.T) {
	result := GetPromptResult{
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
	_ = fs.MkdirAll("/project/.scm/bundles", 0755)

	// Create a test bundle with prompts
	bundleContent := `version: "1.0"
description: Test bundle with prompts
prompts:
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
	_ = afero.WriteFile(fs, "/project/.scm/bundles/dev-tools.yaml", []byte(bundleContent), 0644)

	// Create another bundle with more prompts
	anotherBundle := `version: "1.0"
prompts:
  explain:
    description: Explain code
    content: |
      Explain what this code does
`
	_ = afero.WriteFile(fs, "/project/.scm/bundles/learning.yaml", []byte(anotherBundle), 0644)

	loader := bundles.NewLoader([]string{"/project/.scm/bundles"}, false, bundles.WithFS(fs))
	return fs, loader
}

func TestListPrompts_AllPrompts(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListPrompts(context.Background(), nil, ListPromptsRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, 4, result.Count) // code-review, refactor, commit, explain
	assert.Len(t, result.Prompts, 4)
}

func TestListPrompts_WithQuery(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListPrompts(context.Background(), nil, ListPromptsRequest{
		Query:  "code",
		Loader: loader,
	})

	require.NoError(t, err)
	// Should match "code-review" by name
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, p := range result.Prompts {
		if strings.Contains(p.Name, "code-review") {
			found = true
			break
		}
	}
	assert.True(t, found, "should find code-review prompt")
}

func TestListPrompts_SortAscending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListPrompts(context.Background(), nil, ListPromptsRequest{
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Prompts), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Prompts); i++ {
		assert.LessOrEqual(t, result.Prompts[i-1].Name, result.Prompts[i].Name)
	}
}

func TestListPrompts_SortDescending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListPrompts(context.Background(), nil, ListPromptsRequest{
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Prompts), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Prompts); i++ {
		assert.GreaterOrEqual(t, result.Prompts[i-1].Name, result.Prompts[i].Name)
	}
}

func TestGetPrompt_Success(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	// Use bundle#prompts/name syntax
	result, err := GetPrompt(context.Background(), nil, GetPromptRequest{
		Name:   "dev-tools#prompts/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Contains(t, result.Name, "code-review")
	assert.Contains(t, result.Content, "review")
}

func TestGetPrompt_ValidationError(t *testing.T) {
	_, err := GetPrompt(context.Background(), nil, GetPromptRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestGetPrompt_NotFound(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	_, err := GetPrompt(context.Background(), nil, GetPromptRequest{
		Name:   "nonexistent#prompts/nope",
		Loader: loader,
	})

	require.Error(t, err)
}

func TestGetPrompt_StripsHeaderLines(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := GetPrompt(context.Background(), nil, GetPromptRequest{
		Name:   "dev-tools#prompts/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	// The content should not start with # header after stripping
	assert.False(t, len(result.Content) > 0 && result.Content[0] == '#',
		"content should have header lines stripped")
}

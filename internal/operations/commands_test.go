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

func TestCommandEntry_Fields(t *testing.T) {
	entry := CommandEntry{
		Name:   "my-prompt",
		Source: "local",
	}

	assert.Equal(t, "my-prompt", entry.Name)
	assert.Equal(t, "local", entry.Source)
}

func TestListCommandsRequest_Defaults(t *testing.T) {
	req := ListCommandsRequest{}

	assert.Empty(t, req.Query)
	assert.Empty(t, req.SortBy)
	assert.Empty(t, req.SortOrder)
}

func TestListCommandsResult_Fields(t *testing.T) {
	result := ListCommandsResult{
		Commands: []CommandEntry{
			{Name: "prompt1", Source: "local"},
			{Name: "prompt2", Source: "bundle"},
		},
		Count: 2,
	}

	assert.Len(t, result.Commands, 2)
	assert.Equal(t, 2, result.Count)
}

func TestGetCommandRequest_Validation(t *testing.T) {
	tests := []struct {
		name        string
		req         GetCommandRequest
		shouldError bool
	}{
		{
			name:        "valid request",
			req:         GetCommandRequest{Name: "my-prompt"},
			shouldError: false,
		},
		{
			name:        "empty name",
			req:         GetCommandRequest{Name: ""},
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

func TestGetCommandResult_Fields(t *testing.T) {
	result := GetCommandResult{
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
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	// Create a test bundle with prompts
	bundleContent := `version: "1.0"
description: Test bundle with prompts
commands:
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
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev-tools.yaml", []byte(bundleContent), 0644)

	// Create another bundle with more prompts
	anotherBundle := `version: "1.0"
commands:
  explain:
    description: Explain code
    content: |
      Explain what this code does
`
	_ = afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/learning.yaml", []byte(anotherBundle), 0644)

	loader := bundles.NewLoader([]string{paths.LocalBundlesPath(testBaseDir)}, bundles.WithFS(fs))
	return fs, loader
}

func TestListCommands_AllPrompts(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListCommands(context.Background(), nil, ListCommandsRequest{
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Equal(t, 4, result.Count) // code-review, refactor, commit, explain
	assert.Len(t, result.Commands, 4)
}

func TestListCommands_WithQuery(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListCommands(context.Background(), nil, ListCommandsRequest{
		Query:  "code",
		Loader: loader,
	})

	require.NoError(t, err)
	// Should match "code-review" by name
	assert.GreaterOrEqual(t, result.Count, 1)

	found := false
	for _, p := range result.Commands {
		if strings.Contains(p.Name, "code-review") {
			found = true
			break
		}
	}
	assert.True(t, found, "should find code-review prompt")
}

func TestListCommands_SortAscending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListCommands(context.Background(), nil, ListCommandsRequest{
		SortOrder: "asc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Commands), 2)

	// Verify sorted ascending
	for i := 1; i < len(result.Commands); i++ {
		assert.LessOrEqual(t, result.Commands[i-1].Name, result.Commands[i].Name)
	}
}

func TestListCommands_SortDescending(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := ListCommands(context.Background(), nil, ListCommandsRequest{
		SortOrder: "desc",
		Loader:    loader,
	})

	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Commands), 2)

	// Verify sorted descending
	for i := 1; i < len(result.Commands); i++ {
		assert.GreaterOrEqual(t, result.Commands[i-1].Name, result.Commands[i].Name)
	}
}

func TestGetCommand_Success(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	// Use bundle#commands/name syntax
	result, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:   "dev-tools#commands/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	assert.Contains(t, result.Name, "code-review")
	assert.Contains(t, result.Content, "review")
}

func TestGetCommand_ValidationError(t *testing.T) {
	_, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name: "",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")
}

func TestGetCommand_NotFound(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	_, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:   "nonexistent#commands/nope",
		Loader: loader,
	})

	require.Error(t, err)
}

func TestGetCommand_StripsHeaderLines(t *testing.T) {
	_, loader := setupPromptTestFS(t)

	result, err := GetCommand(context.Background(), nil, GetCommandRequest{
		Name:   "dev-tools#commands/code-review",
		Loader: loader,
	})

	require.NoError(t, err)
	// The content should not start with # header after stripping
	assert.False(t, len(result.Content) > 0 && result.Content[0] == '#',
		"content should have header lines stripped")
}

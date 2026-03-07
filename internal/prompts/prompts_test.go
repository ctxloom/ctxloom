// Prompts tests verify loading, parsing, and filtering of saved prompts.
// Prompts are reusable AI instructions that can be invoked by name, enabling
// consistent behavior for common tasks like code review, refactoring, etc.
package prompts

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestFS creates an in-memory filesystem for isolated testing
func setupTestFS(dirs []string, files map[string][]byte) afero.Fs {
	mfs := afero.NewMemMapFs()
	for _, dir := range dirs {
		_ = mfs.MkdirAll(dir, 0755)
	}
	for path, content := range files {
		_ = afero.WriteFile(mfs, path, content, 0644)
	}
	return mfs
}

// =============================================================================
// Loader Construction Tests
// =============================================================================
// Loaders must be configurable with search directories and filesystem overrides.

func TestNewLoader(t *testing.T) {
	dirs := []string{"/dir1", "/dir2"}
	loader := NewLoader(dirs)

	assert.NotNil(t, loader)
	assert.Equal(t, dirs, loader.searchDirs)
	assert.NotNil(t, loader.fs)
}

func TestNewLoader_WithFS(t *testing.T) {
	// FS injection enables testing without touching real filesystem
	mfs := afero.NewMemMapFs()
	loader := NewLoader([]string{"/dir"}, WithFS(mfs))

	assert.NotNil(t, loader)
	assert.Equal(t, mfs, loader.fs)
}

// =============================================================================
// Prompt Discovery Tests
// =============================================================================
// Find operations locate prompts by name across search directories.

func TestLoader_Find(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/test.yaml": []byte("content: test")},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	t.Run("finds YAML file", func(t *testing.T) {
		path, err := loader.Find("test")
		require.NoError(t, err)
		assert.Equal(t, "/prompts/test.yaml", path)
	})

	t.Run("returns error for not found", func(t *testing.T) {
		// Clear error enables user feedback about missing prompts
		_, err := loader.Find("nonexistent")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}

func TestLoader_Find_YMLExtension(t *testing.T) {
	// Both .yaml and .yml are valid - users shouldn't worry about extension
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/test.yml": []byte("content: test")},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	path, err := loader.Find("test")
	require.NoError(t, err)
	assert.Equal(t, "/prompts/test.yml", path)
}

func TestLoader_Find_MarkdownExtension(t *testing.T) {
	// Legacy markdown prompts are still supported for backward compatibility
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/test.md": []byte("# Test prompt")},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	path, err := loader.Find("test")
	require.NoError(t, err)
	assert.Equal(t, "/prompts/test.md", path)
}

func TestLoader_LoadFile(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/test.yaml": []byte(`
content: |
  Test content here
tags:
  - testing
  - unit
variables:
  - name
`)},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	prompt, err := loader.LoadFile("/prompts/test.yaml")
	require.NoError(t, err)
	assert.Equal(t, "test", prompt.Name)
	assert.Contains(t, prompt.Content, "Test content here")
	assert.Equal(t, []string{"testing", "unit"}, prompt.Tags)
	assert.Equal(t, []string{"name"}, prompt.Variables)
}

func TestLoader_LoadFile_LegacyMarkdown(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/legacy.md": []byte("# Legacy prompt\n\nMarkdown content")},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	prompt, err := loader.LoadFile("/prompts/legacy.md")
	require.NoError(t, err)
	assert.Equal(t, "legacy", prompt.Name)
	assert.Contains(t, prompt.Content, "Legacy prompt")
}

func TestLoader_Load(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{"/prompts/mytest.yaml": []byte("content: My test content")},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	prompt, err := loader.Load("mytest")
	require.NoError(t, err)
	assert.Equal(t, "mytest", prompt.Name)
	assert.Equal(t, "My test content", prompt.Content)
}

// =============================================================================
// Prompt Listing Tests
// =============================================================================
// List operations enable prompt discovery and filtering for MCP tools.

func TestLoader_List(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{
			"/prompts/prompt1.yaml": []byte("content: One\ntags:\n  - first"),
			"/prompts/prompt2.yaml": []byte("content: Two"),
		},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	infos, err := loader.List()
	require.NoError(t, err)
	assert.Len(t, infos, 2)

	var found bool
	for _, info := range infos {
		if info.Name == "prompt1" {
			found = true
			assert.Equal(t, []string{"first"}, info.Tags)
		}
	}
	assert.True(t, found)
}

func TestLoader_List_SkipsHiddenFiles(t *testing.T) {
	// Hidden files (dotfiles) are typically editor/OS artifacts, not prompts
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{
			"/prompts/visible.yaml": []byte("content: visible"),
			"/prompts/.hidden.yaml": []byte("content: hidden"),
		},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	infos, err := loader.List()
	require.NoError(t, err)

	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	assert.Contains(t, names, "visible")
	assert.NotContains(t, names, ".hidden")
}

// =============================================================================
// Tag Filtering Tests
// =============================================================================
// Tags enable organizing prompts by purpose and filtering for specific use cases.

func TestLoader_ListByTags(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{
			"/prompts/tagged.yaml":   []byte("content: Tagged\ntags:\n  - important"),
			"/prompts/untagged.yaml": []byte("content: Untagged"),
		},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	t.Run("filters by tag", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"important"})
		require.NoError(t, err)
		assert.Len(t, infos, 1)
		assert.Equal(t, "tagged", infos[0].Name)
	})

	t.Run("empty tags returns all", func(t *testing.T) {
		infos, err := loader.ListByTags(nil)
		require.NoError(t, err)
		assert.Len(t, infos, 2)
	})

	t.Run("case insensitive", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"IMPORTANT"})
		require.NoError(t, err)
		assert.Len(t, infos, 1)
	})
}

func TestLoader_LoadByTags(t *testing.T) {
	mfs := setupTestFS(
		[]string{"/prompts"},
		map[string][]byte{
			"/prompts/match.yaml":   []byte("content: Matched\ntags:\n  - target"),
			"/prompts/nomatch.yaml": []byte("content: Not matched\ntags:\n  - other"),
		},
	)

	loader := NewLoader([]string{"/prompts"}, WithFS(mfs))

	prompts, err := loader.LoadByTags([]string{"target"})
	require.NoError(t, err)
	assert.Len(t, prompts, 1)
	assert.Equal(t, "Matched", prompts[0].Content)
}

func TestParsePrompt(t *testing.T) {
	t.Run("parses YAML", func(t *testing.T) {
		data := []byte(`
content: YAML content
tags:
  - test
`)
		prompt, err := ParsePrompt(data, "test.yaml")
		require.NoError(t, err)
		assert.Equal(t, "test", prompt.Name)
		assert.Equal(t, "YAML content", prompt.Content)
		assert.Equal(t, []string{"test"}, prompt.Tags)
	})

	t.Run("parses legacy markdown", func(t *testing.T) {
		data := []byte("# Markdown content")
		prompt, err := ParsePrompt(data, "legacy.md")
		require.NoError(t, err)
		assert.Equal(t, "legacy", prompt.Name)
		assert.Equal(t, "# Markdown content", prompt.Content)
	})

	t.Run("invalid YAML returns error", func(t *testing.T) {
		data := []byte("invalid: yaml: [[")
		_, err := ParsePrompt(data, "bad.yaml")
		require.Error(t, err)
	})
}

func TestPrompt_HasTag(t *testing.T) {
	p := &Prompt{Tags: []string{"Go", "Testing"}}

	assert.True(t, p.HasTag("go"))
	assert.True(t, p.HasTag("GO"))
	assert.True(t, p.HasTag("testing"))
	assert.False(t, p.HasTag("python"))
}

func TestPrompt_HasAnyTag(t *testing.T) {
	p := &Prompt{Tags: []string{"go", "testing"}}

	assert.True(t, p.HasAnyTag([]string{"python", "go"}))
	assert.True(t, p.HasAnyTag([]string{"testing"}))
	assert.False(t, p.HasAnyTag([]string{"python", "java"}))
	assert.False(t, p.HasAnyTag(nil))
}

func TestCombinePrompts(t *testing.T) {
	t.Run("combines multiple prompts", func(t *testing.T) {
		prompts := []*Prompt{
			{Content: "First prompt"},
			{Content: "Second prompt"},
		}
		result := CombinePrompts(prompts)
		assert.Contains(t, result, "First prompt")
		assert.Contains(t, result, "Second prompt")
		assert.Contains(t, result, "---")
	})

	t.Run("skips empty content", func(t *testing.T) {
		prompts := []*Prompt{
			{Content: "Has content"},
			{Content: ""},
			{Content: "Also has content"},
		}
		result := CombinePrompts(prompts)
		assert.Contains(t, result, "Has content")
		assert.Contains(t, result, "Also has content")
	})

	t.Run("empty prompts", func(t *testing.T) {
		result := CombinePrompts(nil)
		assert.Empty(t, result)
	})
}

func TestPromptTemplate(t *testing.T) {
	tmpl := PromptTemplate("my-prompt")

	assert.Contains(t, tmpl, "name: my-prompt")
	assert.Contains(t, tmpl, "My Prompt") // Title cased
	assert.Contains(t, tmpl, "content:")
	assert.Contains(t, tmpl, "tags:")
}

func TestToTitleCase(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"hello-world", "Hello World"},
		{"snake_case", "Snake Case"},
		{"already Title", "Already Title"},
		{"UPPER", "Upper"},
		{"single", "Single"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := toTitleCase(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

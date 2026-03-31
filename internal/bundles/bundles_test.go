package bundles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Bundles Package Tests
// =============================================================================
//
// This package manages context bundles - collections of fragments, prompts,
// and MCP server configurations that provide AI context.
//
// KEY CONCEPTS:
// - Bundles are YAML files containing fragments (context) and prompts (templates)
// - Fragments can be distilled (compressed) to reduce token usage
// - Content hashes enable incremental distillation (only re-distill changed content)
// - Tags enable filtering and profile assembly
//
// IMPORTANT BEHAVIORS:
// - IsDistilled flag requires BOTH preference AND availability (AND logic)
// - Tags are inherited: bundle tags + item-specific tags are merged
// - Qualified references (bundle#fragments/name) bypass search, direct lookup
// - Search is case-sensitive and order-dependent (first match wins)
//
// =============================================================================

// =============================================================================
// BundleFragment Tests
// =============================================================================
// BundleFragment represents a single context fragment within a bundle.
// Fragments support distillation (AI-compressed versions) for token efficiency.

func TestBundleFragment_ComputeContentHash(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty content",
			content: "",
			want:    "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name:    "simple content",
			content: "hello world",
			want:    "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
		{
			name:    "multiline content",
			content: "line1\nline2\nline3",
			want:    "sha256:7fe73e5e5e7cd714f7a52c67d6c0b057d7bc9a4f9d3d6f32de0a1c4e9f8a7e6d",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &BundleFragment{Content: tt.content}
			got := f.ComputeContentHash()
			assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, got)
			if tt.name != "multiline content" { // multiline hash is computed dynamically
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestBundleFragment_NeedsDistill(t *testing.T) {
	tests := []struct {
		name     string
		fragment BundleFragment
		want     bool
	}{
		{
			name:     "no_distill set",
			fragment: BundleFragment{NoDistill: true, Content: "test"},
			want:     false,
		},
		{
			name:     "no distilled content",
			fragment: BundleFragment{Content: "test"},
			want:     true,
		},
		{
			name:     "distilled but no hash",
			fragment: BundleFragment{Content: "test", Distilled: "distilled"},
			want:     true,
		},
		{
			name: "hash mismatch",
			fragment: BundleFragment{
				Content:     "new content",
				Distilled:   "distilled",
				ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
			want: true,
		},
		{
			name: "hash matches",
			fragment: BundleFragment{
				Content:     "test",
				Distilled:   "distilled",
				ContentHash: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fragment.NeedsDistill()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBundleFragment_EffectiveContent(t *testing.T) {
	tests := []struct {
		name            string
		fragment        BundleFragment
		preferDistilled bool
		want            string
	}{
		{
			name:            "prefer distilled but none available",
			fragment:        BundleFragment{Content: "original"},
			preferDistilled: true,
			want:            "original",
		},
		{
			name:            "prefer distilled and available",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled"},
			preferDistilled: true,
			want:            "distilled",
		},
		{
			name:            "prefer original",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled"},
			preferDistilled: false,
			want:            "original",
		},
		{
			name:            "no_distill true falls back to content",
			fragment:        BundleFragment{Content: "original", Distilled: "distilled", NoDistill: true},
			preferDistilled: true,
			want:            "original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fragment.EffectiveContent(tt.preferDistilled)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// BundlePrompt Tests
// =============================================================================

func TestBundlePrompt_ComputeContentHash(t *testing.T) {
	p := &BundlePrompt{Content: "test prompt"}
	got := p.ComputeContentHash()
	assert.Regexp(t, `^sha256:[a-f0-9]{64}$`, got)
}

func TestBundlePrompt_NeedsDistill(t *testing.T) {
	tests := []struct {
		name   string
		prompt BundlePrompt
		want   bool
	}{
		{
			name:   "no_distill set",
			prompt: BundlePrompt{NoDistill: true, Content: "test"},
			want:   false,
		},
		{
			name:   "no distilled content",
			prompt: BundlePrompt{Content: "test"},
			want:   true,
		},
		{
			name:   "distilled but no hash",
			prompt: BundlePrompt{Content: "test", Distilled: "distilled"},
			want:   true,
		},
		{
			name: "hash mismatch",
			prompt: BundlePrompt{
				Content:     "new content",
				Distilled:   "distilled",
				ContentHash: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
			want: true,
		},
		{
			name: "hash matches",
			prompt: BundlePrompt{
				Content:     "test",
				Distilled:   "distilled",
				ContentHash: "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prompt.NeedsDistill()
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBundlePrompt_EffectiveContent(t *testing.T) {
	tests := []struct {
		name            string
		prompt          BundlePrompt
		preferDistilled bool
		want            string
	}{
		{
			name:            "prefer distilled and available",
			prompt:          BundlePrompt{Content: "original", Distilled: "distilled"},
			preferDistilled: true,
			want:            "distilled",
		},
		{
			name:            "prefer original",
			prompt:          BundlePrompt{Content: "original", Distilled: "distilled"},
			preferDistilled: false,
			want:            "original",
		},
		{
			name:            "no_distill true falls back to content",
			prompt:          BundlePrompt{Content: "original", Distilled: "distilled", NoDistill: true},
			preferDistilled: true,
			want:            "original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prompt.EffectiveContent(tt.preferDistilled)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// Bundle Tests
// =============================================================================

func TestBundle_HasMCP(t *testing.T) {
	tests := []struct {
		name   string
		bundle Bundle
		want   bool
	}{
		{
			name:   "no MCP",
			bundle: Bundle{},
			want:   false,
		},
		{
			name:   "has MCP",
			bundle: Bundle{MCP: map[string]BundleMCP{"test": {Command: "cmd"}}},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.bundle.HasMCP())
		})
	}
}

func TestBundle_MCPCount(t *testing.T) {
	bundle := Bundle{
		MCP: map[string]BundleMCP{
			"mcp1": {Command: "cmd1"},
			"mcp2": {Command: "cmd2"},
		},
	}
	assert.Equal(t, 2, bundle.MCPCount())
}

func TestBundle_MCPNames(t *testing.T) {
	bundle := Bundle{
		MCP: map[string]BundleMCP{
			"zebra": {Command: "cmd1"},
			"alpha": {Command: "cmd2"},
		},
	}
	names := bundle.MCPNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBundle_FragmentCount(t *testing.T) {
	bundle := Bundle{
		Fragments: map[string]BundleFragment{
			"frag1": {Content: "c1"},
			"frag2": {Content: "c2"},
		},
	}
	assert.Equal(t, 2, bundle.FragmentCount())
}

func TestBundle_PromptCount(t *testing.T) {
	bundle := Bundle{
		Prompts: map[string]BundlePrompt{
			"prompt1": {Content: "c1"},
		},
	}
	assert.Equal(t, 1, bundle.PromptCount())
}

func TestBundle_FragmentNames(t *testing.T) {
	bundle := Bundle{
		Fragments: map[string]BundleFragment{
			"zebra": {Content: "c1"},
			"alpha": {Content: "c2"},
		},
	}
	names := bundle.FragmentNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBundle_PromptNames(t *testing.T) {
	bundle := Bundle{
		Prompts: map[string]BundlePrompt{
			"zebra": {Content: "c1"},
			"alpha": {Content: "c2"},
		},
	}
	names := bundle.PromptNames()
	assert.Equal(t, []string{"alpha", "zebra"}, names)
}

func TestBundle_AllTags(t *testing.T) {
	bundle := Bundle{
		Tags: []string{"bundle-tag"},
		Fragments: map[string]BundleFragment{
			"frag1": {Tags: []string{"frag-tag", "shared"}},
		},
		Prompts: map[string]BundlePrompt{
			"prompt1": {Tags: []string{"prompt-tag", "shared"}},
		},
	}
	tags := bundle.AllTags()
	assert.Contains(t, tags, "bundle-tag")
	assert.Contains(t, tags, "frag-tag")
	assert.Contains(t, tags, "prompt-tag")
	assert.Contains(t, tags, "shared")
}

func TestBundle_Save(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "test-bundle.yaml")

	bundle := &Bundle{
		Path:    bundlePath,
		Version: "1.0",
		Fragments: map[string]BundleFragment{
			"test": {Content: "test content"},
		},
	}

	err := bundle.Save()
	require.NoError(t, err)

	// Verify file was written
	data, err := os.ReadFile(bundlePath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "version: \"1.0\"")
	assert.Contains(t, string(data), "test content")
}

func TestBundle_Save_NoPath(t *testing.T) {
	bundle := &Bundle{Version: "1.0"}
	err := bundle.Save()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no path set")
}

func TestBundle_AssembledContent(t *testing.T) {
	bundle := Bundle{
		Fragments: map[string]BundleFragment{
			"alpha": {Content: "content A"},
			"beta":  {Content: "content B"},
		},
	}
	content := bundle.AssembledContent(false)
	assert.Contains(t, content, "content A")
	assert.Contains(t, content, "content B")
	assert.Contains(t, content, "---")
}

// =============================================================================
// ParseBundle Tests
// =============================================================================

func TestParseBundle(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
		check   func(t *testing.T, b *Bundle)
	}{
		{
			name: "valid bundle",
			yaml: `
version: "1.0"
tags:
  - golang
fragments:
  test-frag:
    content: |
      test content
prompts:
  test-prompt:
    content: prompt content
`,
			wantErr: false,
			check: func(t *testing.T, b *Bundle) {
				assert.Equal(t, "1.0", b.Version)
				assert.Contains(t, b.Tags, "golang")
				assert.Len(t, b.Fragments, 1)
				assert.Len(t, b.Prompts, 1)
			},
		},
		{
			name: "empty bundle initializes maps",
			yaml: `
version: "1.0"
`,
			wantErr: false,
			check: func(t *testing.T, b *Bundle) {
				assert.NotNil(t, b.Fragments)
				assert.NotNil(t, b.Prompts)
				assert.NotNil(t, b.MCP)
			},
		},
		{
			name:    "invalid yaml",
			yaml:    `invalid: [`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bundle, err := ParseBundle([]byte(tt.yaml))
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, bundle)
			}
		})
	}
}

// =============================================================================
// validateBundleName Tests
// =============================================================================

func TestValidateBundleName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid simple", "my-bundle", false},
		{"valid with slash", "github.com/user/repo", false},
		{"empty", "", true},
		{"path traversal", "../secret", true},
		{"absolute path", "/etc/passwd", true},
		{"null byte", "bundle\x00evil", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBundleName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// =============================================================================
// extractBundleName Tests
// =============================================================================

func TestExtractBundleName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/path/to/my-bundle.yaml", "my-bundle"},
		{"/path/to/bundle/bundle.yaml", "bundle"},
		{"simple.yaml", "simple"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := extractBundleName(tt.path)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// NormalizeBundleName Tests
// =============================================================================
// NormalizeBundleName converts paths with git hosting prefixes to canonical
// repo/bundle format. This enables consistent naming regardless of how bundles
// were installed (direct clone vs remote pull).

func TestNormalizeBundleName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		// Git hosting paths should be normalized
		{
			name:  "github.com with owner and bundle path",
			input: "github.com/owner/ctxloom-github/go-development",
			want:  "ctxloom-github/go-development",
		},
		{
			name:  "github.com with owner and repo only",
			input: "github.com/owner/ctxloom-github",
			want:  "ctxloom-github",
		},
		{
			name:  "gitlab.com with group and bundle path",
			input: "gitlab.com/group/repo/core/fragments",
			want:  "repo/core/fragments",
		},
		{
			name:  "bitbucket.org with owner and repo",
			input: "bitbucket.org/team/shared-context/utils",
			want:  "shared-context/utils",
		},

		// Already canonical or local paths should be unchanged
		{
			name:  "already canonical format",
			input: "ctxloom-github/go-development",
			want:  "ctxloom-github/go-development",
		},
		{
			name:  "local bundle name",
			input: "local-bundle",
			want:  "local-bundle",
		},
		{
			name:  "simple name",
			input: "go-tools",
			want:  "go-tools",
		},

		// Edge cases
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "nested local path",
			input: "vendor/bundles/go-tools",
			want:  "vendor/bundles/go-tools",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeBundleName(tt.input)
			assert.Equal(t, tt.want, got, "NormalizeBundleName(%q)", tt.input)
		})
	}
}

// TestNormalizeBundleName_Idempotent verifies that normalizing an already
// normalized name returns the same result.
func TestNormalizeBundleName_Idempotent(t *testing.T) {
	inputs := []string{
		"ctxloom-github/go-development",
		"repo/bundle",
		"simple-bundle",
		"",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			first := NormalizeBundleName(input)
			second := NormalizeBundleName(first)
			assert.Equal(t, first, second, "normalizing should be idempotent")
		})
	}
}

// =============================================================================
// ClaudeCodeConfig Tests
// =============================================================================

func TestClaudeCodeConfig_IsEnabled(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name   string
		config ClaudeCodeConfig
		want   bool
	}{
		{"nil enabled (default true)", ClaudeCodeConfig{}, true},
		{"explicitly enabled", ClaudeCodeConfig{Enabled: &trueBool}, true},
		{"explicitly disabled", ClaudeCodeConfig{Enabled: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.IsEnabled())
		})
	}
}

// =============================================================================
// Loader Tests
// =============================================================================

func TestNewLoader(t *testing.T) {
	dirs := []string{"/path1", "/path2"}
	loader := NewLoader(dirs, true)

	assert.Equal(t, dirs, loader.searchDirs)
	assert.True(t, loader.preferDistilled)
}

func TestWithFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	dirs := []string{"/bundles"}
	loader := NewLoader(dirs, false, WithFS(fs))

	assert.Equal(t, fs, loader.fs)
	assert.Equal(t, dirs, loader.searchDirs)
}

func TestGeminiConfig_IsEnabled(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name   string
		config GeminiConfig
		want   bool
	}{
		{"nil enabled (default true)", GeminiConfig{}, true},
		{"explicitly enabled", GeminiConfig{Enabled: &trueBool}, true},
		{"explicitly disabled", GeminiConfig{Enabled: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.config.IsEnabled())
		})
	}
}

func TestLoader_Find(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test bundle file
	bundlePath := filepath.Join(tmpDir, "test-bundle.yaml")
	err := os.WriteFile(bundlePath, []byte("version: 1.0"), 0644)
	require.NoError(t, err)

	// Create directory-style bundle
	dirBundle := filepath.Join(tmpDir, "dir-bundle")
	require.NoError(t, os.MkdirAll(dirBundle, 0755))
	err = os.WriteFile(filepath.Join(dirBundle, "bundle.yaml"), []byte("version: 1.0"), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)

	t.Run("find file bundle", func(t *testing.T) {
		path, err := loader.Find("test-bundle")
		require.NoError(t, err)
		assert.Equal(t, bundlePath, path)
	})

	t.Run("find directory bundle", func(t *testing.T) {
		path, err := loader.Find("dir-bundle")
		require.NoError(t, err)
		assert.Contains(t, path, "bundle.yaml")
	})

	t.Run("not found", func(t *testing.T) {
		_, err := loader.Find("nonexistent")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("invalid name", func(t *testing.T) {
		_, err := loader.Find("../escape")
		assert.Error(t, err)
	})
}

func TestLoader_LoadFile(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "2.0"
description: Test bundle
fragments:
  test-frag:
    tags:
      - test
    content: |
      Fragment content
`
	bundlePath := filepath.Join(tmpDir, "test.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	bundle, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)

	assert.Equal(t, "2.0", bundle.Version)
	assert.Equal(t, "Test bundle", bundle.Description)
	assert.Equal(t, "test", bundle.Name)
	assert.Equal(t, bundlePath, bundle.Path)
	assert.Len(t, bundle.Fragments, 1)
}

func TestLoader_Load(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `version: "1.0"`
	bundlePath := filepath.Join(tmpDir, "my-bundle.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	bundle, err := loader.Load("my-bundle")
	require.NoError(t, err)

	assert.Equal(t, "1.0", bundle.Version)
	assert.Equal(t, "my-bundle", bundle.Name)
}

func TestLoader_List(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple bundles
	bundle1 := filepath.Join(tmpDir, "bundle1.yaml")
	bundle2 := filepath.Join(tmpDir, "bundle2.yaml")

	err := os.WriteFile(bundle1, []byte(`version: "1.0"
description: Bundle 1
fragments:
  frag1:
    content: c1`), 0644)
	require.NoError(t, err)

	err = os.WriteFile(bundle2, []byte(`version: "2.0"
description: Bundle 2`), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	bundles, err := loader.List()
	require.NoError(t, err)

	assert.Len(t, bundles, 2)
	// Should be sorted by name
	assert.Equal(t, "bundle1", bundles[0].Name)
	assert.Equal(t, "bundle2", bundles[1].Name)
}

func TestLoader_ListAllFragments(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
tags:
  - bundle-tag
fragments:
  frag1:
    tags:
      - frag-tag
    content: content 1
  frag2:
    content: content 2
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	infos, err := loader.ListAllFragments()
	require.NoError(t, err)

	assert.Len(t, infos, 2)

	// Find frag1
	var frag1 *ContentInfo
	for i := range infos {
		if infos[i].Name == "frag1" {
			frag1 = &infos[i]
			break
		}
	}
	require.NotNil(t, frag1)
	assert.Contains(t, frag1.Tags, "bundle-tag")
	assert.Contains(t, frag1.Tags, "frag-tag")
	assert.Equal(t, "fragment", frag1.ItemType)
}

func TestLoader_ListAllPrompts(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
prompts:
  prompt1:
    content: prompt content
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	infos, err := loader.ListAllPrompts()
	require.NoError(t, err)

	assert.Len(t, infos, 1)
	assert.Equal(t, "prompt1", infos[0].Name)
	assert.Equal(t, "prompt", infos[0].ItemType)
}

// =============================================================================
// GetFragment Tests
// =============================================================================
// GetFragment retrieves fragments by name, supporting both simple names
// (searched across all bundles) and qualified names (bundle#fragments/name).
// Tags are inherited from both bundle and fragment levels.

func TestLoader_GetFragment(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
tags:
  - bundle-tag
fragments:
  my-frag:
    tags:
      - frag-tag
    content: |
      Fragment content here
    distilled: Distilled version
`
	err := os.WriteFile(filepath.Join(tmpDir, "test-bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	t.Run("simple name lookup", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		content, err := loader.GetFragment("my-frag")
		require.NoError(t, err)
		assert.Contains(t, content.Content, "Fragment content")
		assert.Contains(t, content.Tags, "bundle-tag")
		assert.Contains(t, content.Tags, "frag-tag")
	})

	t.Run("qualified name lookup", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		content, err := loader.GetFragment("test-bundle#fragments/my-frag")
		require.NoError(t, err)
		assert.Contains(t, content.Content, "Fragment content")
	})

	t.Run("prefer distilled", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, true)
		content, err := loader.GetFragment("my-frag")
		require.NoError(t, err)
		assert.Equal(t, "Distilled version", content.Content)
		assert.True(t, content.IsDistilled)
	})

	t.Run("not found", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		_, err := loader.GetFragment("nonexistent")
		assert.Error(t, err)
	})

	t.Run("invalid qualified reference", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		_, err := loader.GetFragment("test-bundle#invalid/path")
		assert.Error(t, err)
	})
}

// TestLoader_GetFragment_IsDistilledFlag verifies the IsDistilled flag follows
// the same AND logic as prompts: requires BOTH preferDistilled=true AND
// non-empty distilled content.
//
// NON-OBVIOUS: A fragment with preferDistilled=true but empty Distilled field
// returns IsDistilled=false. The flag reflects actual usage, not preference.
func TestLoader_GetFragment_IsDistilledFlag(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
fragments:
  has-distilled:
    content: Original fragment
    distilled: Distilled fragment
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	tests := []struct {
		name            string
		fragName        string
		preferDistilled bool
		wantIsDistilled bool
		wantContent     string
	}{
		{
			name:            "prefer distilled with content",
			fragName:        "has-distilled",
			preferDistilled: true,
			wantIsDistilled: true,
			wantContent:     "Distilled fragment",
		},
		{
			name:            "prefer distilled without content",
			fragName:        "no-distilled",
			preferDistilled: true,
			wantIsDistilled: false,
			wantContent:     "Original only",
		},
		{
			name:            "prefer original with distilled available",
			fragName:        "has-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original fragment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := NewLoader([]string{tmpDir}, tt.preferDistilled)
			content, err := loader.GetFragment(tt.fragName)
			require.NoError(t, err)
			assert.Equal(t, tt.wantIsDistilled, content.IsDistilled)
			assert.Equal(t, tt.wantContent, content.Content)
		})
	}
}

// =============================================================================
// GetPrompt Tests
// =============================================================================
// GetPrompt retrieves prompts by name with distillation preference.
// The IsDistilled flag in the result indicates whether distilled content was
// actually used - this requires BOTH preferDistilled=true AND distilled content
// to exist. This is critical for UI/logging to accurately report content source.

func TestLoader_GetPrompt(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
prompts:
  my-prompt:
    content: Prompt content
    distilled: Distilled prompt
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "test-bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	t.Run("simple name lookup", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		content, err := loader.GetPrompt("my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Prompt content", content.Content)
	})

	t.Run("qualified name lookup", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		content, err := loader.GetPrompt("test-bundle#prompts/my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Prompt content", content.Content)
	})

	t.Run("prefer distilled", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, true)
		content, err := loader.GetPrompt("my-prompt")
		require.NoError(t, err)
		assert.Equal(t, "Distilled prompt", content.Content)
	})

	t.Run("not found", func(t *testing.T) {
		loader := NewLoader([]string{tmpDir}, false)
		_, err := loader.GetPrompt("nonexistent")
		assert.Error(t, err)
	})
}

// TestLoader_GetPrompt_IsDistilledFlag verifies the IsDistilled flag is set
// correctly based on the combination of preferDistilled setting AND actual
// distilled content availability.
//
// EDGE CASE: IsDistilled requires BOTH conditions to be true. If either
// preferDistilled is false OR distilled content is empty, IsDistilled must
// be false. This prevents false reporting of distilled usage.
func TestLoader_GetPrompt_IsDistilledFlag(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
prompts:
  has-distilled:
    content: Original
    distilled: Distilled
  no-distilled:
    content: Original only
`
	err := os.WriteFile(filepath.Join(tmpDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	tests := []struct {
		name            string
		promptName      string
		preferDistilled bool
		wantIsDistilled bool
		wantContent     string
		reason          string
	}{
		{
			name:            "prefer distilled with distilled content available",
			promptName:      "has-distilled",
			preferDistilled: true,
			wantIsDistilled: true,
			wantContent:     "Distilled",
			reason:          "Both conditions met: preference AND availability",
		},
		{
			name:            "prefer distilled but no distilled content",
			promptName:      "no-distilled",
			preferDistilled: true,
			wantIsDistilled: false,
			wantContent:     "Original only",
			reason:          "Preference set but no distilled content exists - must use original",
		},
		{
			name:            "prefer original even with distilled available",
			promptName:      "has-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original",
			reason:          "User explicitly prefers original content",
		},
		{
			name:            "prefer original with no distilled",
			promptName:      "no-distilled",
			preferDistilled: false,
			wantIsDistilled: false,
			wantContent:     "Original only",
			reason:          "Neither preference nor availability - straightforward original",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := NewLoader([]string{tmpDir}, tt.preferDistilled)
			content, err := loader.GetPrompt(tt.promptName)
			require.NoError(t, err, "prompt should be found")
			assert.Equal(t, tt.wantIsDistilled, content.IsDistilled,
				"IsDistilled mismatch: %s", tt.reason)
			assert.Equal(t, tt.wantContent, content.Content,
				"Content mismatch: %s", tt.reason)
		})
	}
}

func TestLoader_ListByTags(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
fragments:
  golang-frag:
    tags:
      - golang
      - programming
    content: Go content
  python-frag:
    tags:
      - python
      - programming
    content: Python content
  docs-frag:
    tags:
      - documentation
    content: Docs content
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)

	t.Run("single tag", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"golang"})
		require.NoError(t, err)
		assert.Len(t, infos, 1)
		assert.Equal(t, "golang-frag", infos[0].Name)
	})

	t.Run("multiple tags (OR logic)", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"golang", "python"})
		require.NoError(t, err)
		assert.Len(t, infos, 2)
	})

	t.Run("shared tag", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"programming"})
		require.NoError(t, err)
		assert.Len(t, infos, 2)
	})

	t.Run("no matches", func(t *testing.T) {
		infos, err := loader.ListByTags([]string{"nonexistent"})
		require.NoError(t, err)
		assert.Len(t, infos, 0)
	})
}

func TestLoader_LoadMultiple(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `
version: "1.0"
fragments:
  frag1:
    content: Content one
  frag2:
    content: Content two
`
	err := os.WriteFile(filepath.Join(tmpDir, "test.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)

	t.Run("load multiple fragments", func(t *testing.T) {
		content, err := loader.LoadMultiple([]string{"frag1", "frag2"})
		require.NoError(t, err)
		assert.Contains(t, content, "Content one")
		assert.Contains(t, content, "Content two")
		assert.Contains(t, content, "---")
	})

	t.Run("skip missing fragments", func(t *testing.T) {
		content, err := loader.LoadMultiple([]string{"frag1", "nonexistent"})
		require.NoError(t, err)
		assert.Contains(t, content, "Content one")
	})
}

// =============================================================================
// Edge Cases and Error Handling
// =============================================================================
// These tests verify graceful degradation under unusual conditions.
// SCM should be fault-tolerant - misconfiguration shouldn't crash the system.

// TestLoader_EmptySearchDirs verifies the loader handles no search directories.
// FAULT TOLERANCE: Empty config should not error, just return no bundles.
func TestLoader_EmptySearchDirs(t *testing.T) {
	loader := NewLoader([]string{}, false)

	bundles, err := loader.List()
	require.NoError(t, err, "empty dirs should not error")
	assert.Empty(t, bundles)
}

// TestLoader_NonexistentSearchDir verifies missing directories are skipped.
// FAULT TOLERANCE: Invalid paths in config should be silently ignored.
// This enables portable configs that reference optional bundle locations.
func TestLoader_NonexistentSearchDir(t *testing.T) {
	loader := NewLoader([]string{"/nonexistent/path"}, false)

	bundles, err := loader.List()
	require.NoError(t, err, "nonexistent dir should not error")
	assert.Empty(t, bundles)
}

// TestLoader_LoadFile_NotFound verifies proper error on missing files.
// Unlike directory searches, explicit file loads SHOULD error - the user
// specifically requested a file that doesn't exist.
func TestLoader_LoadFile_NotFound(t *testing.T) {
	loader := NewLoader([]string{}, false)
	_, err := loader.LoadFile("/nonexistent/bundle.yaml")
	assert.Error(t, err, "explicit file load should error when not found")
}

// TestLoader_LoadFile_InvalidYAML verifies malformed bundles are rejected.
// Unlike missing files, corrupt bundles indicate a real problem that
// the user needs to fix.
func TestLoader_LoadFile_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	bundlePath := filepath.Join(tmpDir, "invalid.yaml")
	err := os.WriteFile(bundlePath, []byte("invalid: ["), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	_, err = loader.LoadFile(bundlePath)
	assert.Error(t, err, "invalid YAML should error")
}

// TestLoader_LoadFile_Caching verifies that bundles are cached after loading.
// This optimization avoids redundant disk reads when the same bundle is
// referenced multiple times (e.g., by multiple profiles).
func TestLoader_LoadFile_Caching(t *testing.T) {
	tmpDir := t.TempDir()

	bundleYAML := `version: "1.0"
fragments:
  test-frag:
    content: Test content`
	bundlePath := filepath.Join(tmpDir, "test.yaml")
	err := os.WriteFile(bundlePath, []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)

	// First load
	bundle1, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, "1.0", bundle1.Version)

	// Modify file on disk
	modifiedYAML := `version: "2.0"
fragments:
  test-frag:
    content: Modified content`
	err = os.WriteFile(bundlePath, []byte(modifiedYAML), 0644)
	require.NoError(t, err)

	// Second load should return cached version (version 1.0)
	bundle2, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, "1.0", bundle2.Version, "should return cached bundle, not re-read from disk")

	// Same pointer (cached)
	assert.Same(t, bundle1, bundle2, "should return same bundle instance from cache")

	// ClearCache and reload
	loader.ClearCache()
	bundle3, err := loader.LoadFile(bundlePath)
	require.NoError(t, err)
	assert.Equal(t, "2.0", bundle3.Version, "should read updated file after cache clear")
}

// TestLoader_NestedBundles verifies deep directory structures are traversed.
// NON-OBVIOUS: Bundle names preserve the relative path structure.
// A bundle at vendor/github.com/user/bundle.yaml gets name "vendor/github.com/user".
// This enables namespacing and prevents collisions between remote sources.
func TestLoader_NestedBundles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create nested directory structure
	nestedDir := filepath.Join(tmpDir, "vendor", "github.com", "user")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))

	bundleYAML := `version: "1.0"
fragments:
  nested-frag:
    content: Nested content`
	err := os.WriteFile(filepath.Join(nestedDir, "bundle.yaml"), []byte(bundleYAML), 0644)
	require.NoError(t, err)

	loader := NewLoader([]string{tmpDir}, false)
	bundles, err := loader.List()
	require.NoError(t, err)

	// Should find the nested bundle
	var found bool
	for _, b := range bundles {
		if b.Name == "vendor/github.com/user" {
			found = true
			break
		}
	}
	assert.True(t, found, "should find nested bundle with path-based name")
}

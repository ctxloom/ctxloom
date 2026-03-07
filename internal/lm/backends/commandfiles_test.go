package backends

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benjaminabbitt/scm/internal/bundles"
)

func TestTransformMustacheToPositional(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single variable",
			input:    "Review {{file}}",
			expected: "Review $1",
		},
		{
			name:     "two variables",
			input:    "Review {{file}} focusing on {{focus}}",
			expected: "Review $1 focusing on $2",
		},
		{
			name:     "repeated variable",
			input:    "Check {{file}}, then recheck {{file}}",
			expected: "Check $1, then recheck $1",
		},
		{
			name:     "mixed order",
			input:    "First {{a}}, then {{b}}, back to {{a}}",
			expected: "First $1, then $2, back to $1",
		},
		{
			name:     "no variables",
			input:    "Just plain text",
			expected: "Just plain text",
		},
		{
			name:     "multiline",
			input:    "Review {{file}}\n\nFocus: {{focus}}\n\nFile: {{file}}",
			expected: "Review $1\n\nFocus: $2\n\nFile: $1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transformMustacheToPositional(tt.input)
			if result != tt.expected {
				t.Errorf("transformMustacheToPositional(%q)\ngot:  %q\nwant: %q",
					tt.input, result, tt.expected)
			}
		})
	}
}

func TestTransformToClaudeCommand(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		fragment *bundles.LoadedContent
		contains []string
		excludes []string
	}{
		{
			name: "full frontmatter",
			fragment: &bundles.LoadedContent{
				Name:    "review",
				Content: "Review {{file}} for {{focus}}",
				Plugins: bundles.PluginsConfig{
					LM: bundles.LMPluginConfig{
						ClaudeCode: bundles.ClaudeCodeConfig{
							Description:  "Code review",
							ArgumentHint: "[file] [focus]",
							AllowedTools: []string{"Read", "Grep"},
							Model:        "claude-sonnet-4-20250514",
						},
					},
				},
			},
			contains: []string{
				"---",
				"description: Code review",
				"argument-hint: [file] [focus]",
				"allowed-tools: Read, Grep",
				"model: claude-sonnet-4-20250514",
				"Review $1 for $2",
			},
		},
		{
			name: "no frontmatter",
			fragment: &bundles.LoadedContent{
				Name:    "simple",
				Content: "Just do the thing",
			},
			excludes: []string{"---"},
			contains: []string{"Just do the thing"},
		},
		{
			name: "partial frontmatter",
			fragment: &bundles.LoadedContent{
				Name:    "partial",
				Content: "Review the code",
				Plugins: bundles.PluginsConfig{
					LM: bundles.LMPluginConfig{
						ClaudeCode: bundles.ClaudeCodeConfig{
							Description: "Quick review",
						},
					},
				},
			},
			contains: []string{
				"---",
				"description: Quick review",
				"Review the code",
			},
			excludes: []string{
				"argument-hint:",
				"allowed-tools:",
				"model:",
			},
		},
		{
			name: "explicitly enabled",
			fragment: &bundles.LoadedContent{
				Name:    "enabled",
				Content: "Content",
				Plugins: bundles.PluginsConfig{
					LM: bundles.LMPluginConfig{
						ClaudeCode: bundles.ClaudeCodeConfig{
							Enabled:     boolPtr(true),
							Description: "Enabled command",
						},
					},
				},
			},
			contains: []string{"description: Enabled command"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TransformToClaudeCommand(tt.fragment)

			for _, s := range tt.contains {
				if !strings.Contains(result, s) {
					t.Errorf("expected result to contain %q\nresult: %s", s, result)
				}
			}

			for _, s := range tt.excludes {
				if strings.Contains(result, s) {
					t.Errorf("expected result to NOT contain %q\nresult: %s", s, result)
				}
			}
		})
	}
}

func TestClaudeCodeConfigIsEnabled(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		config   bundles.ClaudeCodeConfig
		expected bool
	}{
		{
			name:     "nil enabled (default)",
			config:   bundles.ClaudeCodeConfig{},
			expected: true,
		},
		{
			name:     "explicitly true",
			config:   bundles.ClaudeCodeConfig{Enabled: boolPtr(true)},
			expected: true,
		},
		{
			name:     "explicitly false",
			config:   bundles.ClaudeCodeConfig{Enabled: boolPtr(false)},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsEnabled()
			if result != tt.expected {
				t.Errorf("IsEnabled() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestWriteCommandFiles(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	// Create temp directory
	tmpDir := t.TempDir()

	prompts := []*bundles.LoadedContent{
		{
			Name:    "review",
			Content: "Review {{file}}",
			Plugins: bundles.PluginsConfig{
				LM: bundles.LMPluginConfig{
					ClaudeCode: bundles.ClaudeCodeConfig{
						Description: "Code review",
					},
				},
			},
		},
		{
			Name:    "disabled",
			Content: "This should not be exported",
			Plugins: bundles.PluginsConfig{
				LM: bundles.LMPluginConfig{
					ClaudeCode: bundles.ClaudeCodeConfig{
						Enabled: boolPtr(false),
					},
				},
			},
		},
		{
			Name:    "simple",
			Content: "Simple command",
		},
	}

	err := WriteCommandFiles(tmpDir, prompts)
	if err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	// Check that enabled prompts are exported
	reviewPath := filepath.Join(tmpDir, ".claude", "commands", "scm", "review.md")
	if _, err := os.Stat(reviewPath); os.IsNotExist(err) {
		t.Error("expected review.md to be created")
	}

	// Check that simple (default enabled) is exported
	simplePath := filepath.Join(tmpDir, ".claude", "commands", "scm", "simple.md")
	if _, err := os.Stat(simplePath); os.IsNotExist(err) {
		t.Error("expected simple.md to be created")
	}

	// Check that disabled prompt is NOT exported
	disabledPath := filepath.Join(tmpDir, ".claude", "commands", "scm", "disabled.md")
	if _, err := os.Stat(disabledPath); !os.IsNotExist(err) {
		t.Error("expected disabled.md to NOT be created")
	}

	// Verify content of review.md
	content, err := os.ReadFile(reviewPath)
	if err != nil {
		t.Fatalf("failed to read review.md: %v", err)
	}

	if !strings.Contains(string(content), "description: Code review") {
		t.Error("review.md should contain description")
	}
	if !strings.Contains(string(content), "Review $1") {
		t.Error("review.md should have {{file}} transformed to $1")
	}
}

func TestWriteCommandFilesCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".claude", "commands", "scm")

	// Create a stale file
	_ = os.MkdirAll(scmDir, 0755)
	stalePath := filepath.Join(scmDir, "stale.md")
	_ = os.WriteFile(stalePath, []byte("stale content"), 0644)

	// Write new commands (empty list - should still clean up)
	prompts := []*bundles.LoadedContent{
		{
			Name:    "new",
			Content: "New content",
		},
	}

	err := WriteCommandFiles(tmpDir, prompts)
	if err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	// Stale file should be gone
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected stale.md to be removed")
	}

	// New file should exist
	newPath := filepath.Join(scmDir, "new.md")
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Error("expected new.md to be created")
	}
}

func TestWriteCommandFilesEmptyPrompts(t *testing.T) {
	tmpDir := t.TempDir()
	scmDir := filepath.Join(tmpDir, ".claude", "commands", "scm")

	// Pre-create directory with file
	_ = os.MkdirAll(scmDir, 0755)
	stalePath := filepath.Join(scmDir, "stale.md")
	_ = os.WriteFile(stalePath, []byte("stale content"), 0644)

	// Write with no prompts
	err := WriteCommandFiles(tmpDir, nil)
	if err != nil {
		t.Fatalf("WriteCommandFiles failed: %v", err)
	}

	// Directory should be removed (or at least stale file gone)
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("expected stale.md to be removed")
	}
}

func TestEscapeYAMLString(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "simple"},
		{"with spaces", "with spaces"},
		{"with: colon", `"with: colon"`},
		{"with #hash", `"with #hash"`},
		{" leading space", `" leading space"`},
		{"trailing space ", `"trailing space "`},
		{`has "quotes"`, `"has \"quotes\""`},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := escapeYAMLString(tt.input)
			if result != tt.expected {
				t.Errorf("escapeYAMLString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

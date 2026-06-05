package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// =============================================================================
// Gemini Backend Construction Tests
//
// Gemini is Google's AI CLI tool. These tests verify proper initialization
// and configuration of the Gemini backend.
// =============================================================================

// TestNewGemini_DefaultValues verifies that a new Gemini backend is created
// with sensible defaults for binary path and capabilities.
func TestNewGemini_DefaultValues(t *testing.T) {
	backend := NewGemini(WriteSettings)

	assert.Equal(t, "gemini", backend.Name())
	assert.Equal(t, "1.0.0", backend.Version())
	assert.Equal(t, "gemini", backend.BinaryPath)
	assert.Empty(t, backend.Args)
}

// TestNewGemini_CapabilitiesCorrect verifies that Gemini has the expected
// capabilities - it supports lifecycle, context, MCP, skills, and history.
func TestNewGemini_CapabilitiesCorrect(t *testing.T) {
	backend := NewGemini(WriteSettings)

	assert.NotNil(t, backend.Lifecycle(), "Gemini should support lifecycle hooks")
	assert.NotNil(t, backend.Skills(), "Gemini should support skills/slash commands")
	assert.NotNil(t, backend.Context(), "Gemini should support context injection")
	assert.NotNil(t, backend.MCP(), "Gemini should support MCP servers")
	assert.NotNil(t, backend.History(), "Gemini should support session history")
}

// TestNewGemini_SupportedModes verifies that Gemini supports both interactive
// and oneshot execution modes.
func TestNewGemini_SupportedModes(t *testing.T) {
	backend := NewGemini(WriteSettings)
	modes := backend.SupportedModes()

	assert.Len(t, modes, 2)
	assert.Contains(t, modes, ModeInteractive)
	assert.Contains(t, modes, ModeOneshot)
}

// =============================================================================
// Gemini Argument Building Tests
//
// buildArgs constructs the command-line arguments for the gemini command.
// =============================================================================

// argPair reports whether args contains flag immediately followed by value.
func argPair(args []string, flag, value string) bool {
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}

// argValue returns the value following flag in args, or "" if the flag is
// absent or has no following element.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestGemini_BuildArgs_AutoApprove verifies that auto-approve mode adds
// the --yolo flag.
func TestGemini_BuildArgs_AutoApprove(t *testing.T) {
	backend := NewGemini(WriteSettings)

	args := backend.buildArgs(&ExecuteRequest{AutoApprove: true}, "")

	assert.Contains(t, args, "--yolo")
}

// TestGemini_BuildArgs_Model verifies the resolved model is passed via -m.
func TestGemini_BuildArgs_Model(t *testing.T) {
	backend := NewGemini(WriteSettings)

	args := backend.buildArgs(&ExecuteRequest{}, "gemini-2.5-pro")

	assert.True(t, argPair(args, "-m", "gemini-2.5-pro"), "-m should carry the resolved model")
}

// TestGemini_BuildArgs_InteractivePrompt verifies an interactive prompt uses -i
// (run then stay in the session).
func TestGemini_BuildArgs_InteractivePrompt(t *testing.T) {
	backend := NewGemini(WriteSettings)

	req := &ExecuteRequest{Mode: ModeInteractive, Prompt: &Fragment{Content: "Review this code"}}
	args := backend.buildArgs(req, "")

	assert.True(t, argPair(args, "-i", "Review this code"), "-i should carry the interactive prompt")
}

// TestGemini_BuildArgs_OneshotPrompt verifies a oneshot prompt uses -p (headless
// and exits) rather than -i (which would hang in interactive mode).
func TestGemini_BuildArgs_OneshotPrompt(t *testing.T) {
	backend := NewGemini(WriteSettings)

	req := &ExecuteRequest{Mode: ModeOneshot, Prompt: &Fragment{Content: "distill this"}}
	args := backend.buildArgs(req, "")

	assert.True(t, argPair(args, "-p", "distill this"), "oneshot must use -p")
	assert.NotContains(t, args, "-i", "oneshot must not use interactive -i")
}

// TestGemini_BuildArgs_SkipSetup verifies the minimal/distillation path runs
// read-only (--approval-mode plan) and does not auto-approve.
func TestGemini_BuildArgs_SkipSetup(t *testing.T) {
	backend := NewGemini(WriteSettings)

	req := &ExecuteRequest{SkipSetup: true, AutoApprove: true}
	args := backend.buildArgs(req, "")

	assert.True(t, argPair(args, "--approval-mode", "plan"), "SkipSetup should run read-only")
	assert.NotContains(t, args, "--yolo", "SkipSetup takes precedence over auto-approve")
}

// TestGemini_BuildArgs_NoPrompt verifies that a missing prompt adds neither
// -i nor -p.
func TestGemini_BuildArgs_NoPrompt(t *testing.T) {
	backend := NewGemini(WriteSettings)

	args := backend.buildArgs(&ExecuteRequest{Prompt: nil}, "")

	assert.NotContains(t, args, "-i")
	assert.NotContains(t, args, "-p")
}

// TestGemini_BuildArgs_Combined verifies that multiple options are combined
// correctly.
func TestGemini_BuildArgs_Combined(t *testing.T) {
	backend := NewGemini(WriteSettings)
	backend.Args = []string{"--existing-flag"}

	req := &ExecuteRequest{
		Mode:        ModeInteractive,
		AutoApprove: true,
		Prompt:      &Fragment{Content: "Test prompt"},
	}
	args := backend.buildArgs(req, "gemini-2.5-flash")

	assert.Contains(t, args, "--existing-flag")
	assert.True(t, argPair(args, "-m", "gemini-2.5-flash"))
	assert.Contains(t, args, "--yolo")
	assert.Contains(t, args, "-i")
}

// TestGemini_Configure verifies per-LLM config (binary/args/env) is applied.
func TestGemini_Configure(t *testing.T) {
	backend := NewGemini(WriteSettings)

	trust := true
	backend.Configure(&GeminiConfig{
		BinaryPath:     "/opt/gemini/bin/gemini",
		Args:           []string{"--flag"},
		Env:            map[string]string{"FOO": "bar"},
		TrustWorkspace: &trust,
	})

	assert.Equal(t, "/opt/gemini/bin/gemini", backend.BinaryPath)
	assert.Equal(t, []string{"--flag"}, backend.Args)
	assert.Equal(t, "bar", backend.Env["FOO"])
	assert.Equal(t, "true", backend.Env[geminiTrustWorkspaceEnv], "trust_workspace sets the env override")
}

// =============================================================================
// Gemini Skills Tests
//
// These tests verify that Gemini skills/slash commands are properly
// transformed to TOML format and written to .gemini/commands/ctxloom/.
// =============================================================================

// TestGeminiConfigIsEnabled verifies the opt-out model for Gemini config.
func TestGeminiConfigIsEnabled(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name     string
		config   bundles.GeminiConfig
		expected bool
	}{
		{
			name:     "nil enabled (default)",
			config:   bundles.GeminiConfig{},
			expected: true,
		},
		{
			name:     "explicitly true",
			config:   bundles.GeminiConfig{Enabled: boolPtr(true)},
			expected: true,
		},
		{
			name:     "explicitly false",
			config:   bundles.GeminiConfig{Enabled: boolPtr(false)},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.IsEnabled()
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestTransformToGeminiCommand verifies the TOML transformation.
func TestTransformToGeminiCommand(t *testing.T) {
	tests := []struct {
		name     string
		content  *bundles.LoadedContent
		contains []string
		excludes []string
	}{
		{
			name: "with description",
			content: &bundles.LoadedContent{
				Name:    "review",
				Content: "Review {{args}} for issues",
				LLM: bundles.LLMExports{
					Gemini: bundles.GeminiConfig{
						Description: "Code review command",
					},
				},
			},
			contains: []string{
				"description =",
				"Code review command",
				"prompt =",
				"Review {{args}} for issues",
			},
		},
		{
			name: "no description",
			content: &bundles.LoadedContent{
				Name:    "simple",
				Content: "Just do the thing",
			},
			contains: []string{
				"prompt =",
				"Just do the thing",
			},
			excludes: []string{"description ="},
		},
		{
			name: "multiline content",
			content: &bundles.LoadedContent{
				Name:    "multi",
				Content: "Line one\nLine two\nLine three",
			},
			contains: []string{
				"prompt =",
				"Line one",
				"Line two",
				"Line three",
			},
		},
		{
			name: "special characters",
			content: &bundles.LoadedContent{
				Name:    "special",
				Content: `Review "this" code with 'quotes' and \backslashes`,
			},
			contains: []string{
				"prompt =",
				"Review",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := TransformToGeminiCommand(tt.content)
			assert.NoError(t, err)

			resultStr := string(result)
			for _, s := range tt.contains {
				assert.Contains(t, resultStr, s, "expected result to contain %q", s)
			}

			for _, s := range tt.excludes {
				assert.NotContains(t, resultStr, s, "expected result to NOT contain %q", s)
			}
		})
	}
}

// TestWriteGeminiCommandFiles verifies command file writing.
func TestWriteGeminiCommandFiles(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }
	tmpDir := t.TempDir()

	prompts := []*bundles.LoadedContent{
		{
			Name:    "review",
			Content: "Review {{args}}",
			LLM: bundles.LLMExports{
				Gemini: bundles.GeminiConfig{
					Description: "Code review",
				},
			},
		},
		{
			Name:    "disabled",
			Content: "This should not be exported",
			LLM: bundles.LLMExports{
				Gemini: bundles.GeminiConfig{
					Enabled: boolPtr(false),
				},
			},
		},
		{
			Name:    "simple",
			Content: "Simple command",
		},
	}

	err := WriteGeminiCommandFiles(tmpDir, prompts)
	assert.NoError(t, err)

	// Check that enabled prompts are exported
	reviewPath := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom", "review.toml")
	assert.FileExists(t, reviewPath, "expected review.toml to be created")

	// Check that simple (default enabled) is exported
	simplePath := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom", "simple.toml")
	assert.FileExists(t, simplePath, "expected simple.toml to be created")

	// Check that disabled prompt is NOT exported
	disabledPath := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom", "disabled.toml")
	assert.NoFileExists(t, disabledPath, "expected disabled.toml to NOT be created")

	// Verify content of review.toml
	content, err := os.ReadFile(reviewPath)
	assert.NoError(t, err)

	assert.Contains(t, string(content), "description =")
	assert.Contains(t, string(content), "Code review")
	assert.Contains(t, string(content), "prompt =")
	assert.Contains(t, string(content), "Review {{args}}")
}

// TestWriteGeminiCommandFilesCleanup verifies stale files are removed.
func TestWriteGeminiCommandFilesCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	ctxloomDir := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom")

	// Create a stale file
	err := os.MkdirAll(ctxloomDir, 0755)
	assert.NoError(t, err)

	stalePath := filepath.Join(ctxloomDir, "stale.toml")
	err = os.WriteFile(stalePath, []byte("stale content"), 0644)
	assert.NoError(t, err)

	// Write new commands
	prompts := []*bundles.LoadedContent{
		{
			Name:    "new",
			Content: "New content",
		},
	}

	err = WriteGeminiCommandFiles(tmpDir, prompts)
	assert.NoError(t, err)

	// Stale file should be gone
	assert.NoFileExists(t, stalePath, "expected stale.toml to be removed")

	// New file should exist
	newPath := filepath.Join(ctxloomDir, "new.toml")
	assert.FileExists(t, newPath, "expected new.toml to be created")
}

// TestWriteGeminiCommandFilesEmptyPrompts verifies behavior with no prompts.
func TestWriteGeminiCommandFilesEmptyPrompts(t *testing.T) {
	tmpDir := t.TempDir()
	ctxloomDir := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom")

	// Pre-create directory with file
	err := os.MkdirAll(ctxloomDir, 0755)
	assert.NoError(t, err)

	stalePath := filepath.Join(ctxloomDir, "stale.toml")
	err = os.WriteFile(stalePath, []byte("stale content"), 0644)
	assert.NoError(t, err)

	// Write with no prompts
	err = WriteGeminiCommandFiles(tmpDir, nil)
	assert.NoError(t, err)

	// Stale file should be removed
	assert.NoFileExists(t, stalePath, "expected stale.toml to be removed")
}

// TestTransformToGeminiCommand_RoundTrip verifies TOML output is valid and parseable.
func TestTransformToGeminiCommand_RoundTrip(t *testing.T) {
	content := &bundles.LoadedContent{
		Name:    "test",
		Content: "Review {{args}} for issues\nMultiple lines\nWith special chars: \"quotes\" and 'apostrophes'",
		LLM: bundles.LLMExports{
			Gemini: bundles.GeminiConfig{
				Description: "Test command with special chars: \"quotes\"",
			},
		},
	}

	tomlData, err := TransformToGeminiCommand(content)
	assert.NoError(t, err)

	// Parse it back
	var parsed GeminiCommand
	err = toml.Unmarshal(tomlData, &parsed)
	assert.NoError(t, err)

	// Verify round-trip
	assert.Equal(t, content.LLM.Gemini.Description, parsed.Description)
	assert.Equal(t, content.Content, parsed.Prompt)
}

// TestGeminiSkills_List verifies skill listing functionality.
func TestGeminiSkills_List(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewGemini(WriteSettings)

	// Initially empty
	names, err := backend.skills.List(tmpDir)
	assert.NoError(t, err)
	assert.Empty(t, names)

	// Create some command files
	ctxloomDir := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom")
	err = os.MkdirAll(ctxloomDir, 0755)
	assert.NoError(t, err)

	err = os.WriteFile(filepath.Join(ctxloomDir, "cmd1.toml"), []byte("prompt = \"test\""), 0644)
	assert.NoError(t, err)
	err = os.WriteFile(filepath.Join(ctxloomDir, "cmd2.toml"), []byte("prompt = \"test\""), 0644)
	assert.NoError(t, err)

	// Should list both
	names, err = backend.skills.List(tmpDir)
	assert.NoError(t, err)
	assert.Len(t, names, 2)
	assert.Contains(t, names, "cmd1")
	assert.Contains(t, names, "cmd2")
}

// TestGeminiSkills_Clear verifies skill clearing functionality.
func TestGeminiSkills_Clear(t *testing.T) {
	tmpDir := t.TempDir()
	backend := NewGemini(WriteSettings)

	// Create some command files
	ctxloomDir := filepath.Join(tmpDir, ".gemini", "commands", "ctxloom")
	err := os.MkdirAll(ctxloomDir, 0755)
	assert.NoError(t, err)

	err = os.WriteFile(filepath.Join(ctxloomDir, "cmd.toml"), []byte("prompt = \"test\""), 0644)
	assert.NoError(t, err)

	// Clear should remove directory
	err = backend.skills.Clear(tmpDir)
	assert.NoError(t, err)

	assert.NoDirExists(t, ctxloomDir)
}

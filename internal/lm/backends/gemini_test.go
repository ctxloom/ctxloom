package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
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
	backend := NewGemini()

	assert.Equal(t, "gemini", backend.Name())
	assert.Equal(t, "1.0.0", backend.Version())
	assert.Equal(t, "gemini", backend.BinaryPath)
	assert.Empty(t, backend.Args)
}

// TestNewGemini_CapabilitiesCorrect verifies that Gemini has the expected
// capabilities - it supports lifecycle, context, and MCP but not skills.
func TestNewGemini_CapabilitiesCorrect(t *testing.T) {
	backend := NewGemini()

	assert.NotNil(t, backend.Lifecycle(), "Gemini should support lifecycle hooks")
	assert.Nil(t, backend.Skills(), "Gemini doesn't support skills/slash commands")
	assert.NotNil(t, backend.Context(), "Gemini should support context injection")
	assert.NotNil(t, backend.MCP(), "Gemini should support MCP servers")
}

// TestNewGemini_SupportedModes verifies that Gemini supports both interactive
// and oneshot execution modes.
func TestNewGemini_SupportedModes(t *testing.T) {
	backend := NewGemini()
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

// TestGemini_BuildArgs_AutoApprove verifies that auto-approve mode adds
// the --yolo flag for non-interactive use.
func TestGemini_BuildArgs_AutoApprove(t *testing.T) {
	backend := NewGemini()

	req := &ExecuteRequest{
		AutoApprove: true,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--yolo")
}

// TestGemini_BuildArgs_Prompt verifies that prompt content is passed via
// the -i flag.
func TestGemini_BuildArgs_Prompt(t *testing.T) {
	backend := NewGemini()

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "Review this code"},
	}
	args := backend.buildArgs(req)

	found := false
	for i, arg := range args {
		if arg == "-i" && i+1 < len(args) && args[i+1] == "Review this code" {
			found = true
			break
		}
	}
	assert.True(t, found, "-i flag should be set with prompt")
}

// TestGemini_BuildArgs_NoPrompt verifies that missing prompt doesn't add
// the -i flag.
func TestGemini_BuildArgs_NoPrompt(t *testing.T) {
	backend := NewGemini()

	req := &ExecuteRequest{
		Prompt: nil,
	}
	args := backend.buildArgs(req)

	assert.NotContains(t, args, "-i")
}

// TestGemini_BuildArgs_Combined verifies that multiple options are combined
// correctly.
func TestGemini_BuildArgs_Combined(t *testing.T) {
	backend := NewGemini()
	backend.Args = []string{"--existing-flag"}

	req := &ExecuteRequest{
		AutoApprove: true,
		Prompt:      &Fragment{Content: "Test prompt"},
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--existing-flag")
	assert.Contains(t, args, "--yolo")
	assert.Contains(t, args, "-i")
}

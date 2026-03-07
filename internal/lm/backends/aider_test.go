package backends

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Aider Backend Construction Tests
//
// Aider is an AI pair programming tool. These tests verify proper initialization
// of the Aider backend. Note: This plugin is provided on a best-effort basis.
// =============================================================================

// TestNewAider_DefaultValues verifies that a new Aider backend is created
// with sensible defaults.
func TestNewAider_DefaultValues(t *testing.T) {
	backend := NewAider()

	assert.Equal(t, "aider", backend.Name())
	assert.Equal(t, "1.0.0", backend.Version())
	assert.Equal(t, "aider", backend.BinaryPath)
	assert.Empty(t, backend.Args)
}

// TestNewAider_CapabilitiesCorrect verifies that Aider has limited capabilities.
// It only supports context injection, not lifecycle hooks, skills, or MCP.
func TestNewAider_CapabilitiesCorrect(t *testing.T) {
	backend := NewAider()

	assert.Nil(t, backend.Lifecycle(), "Aider doesn't support lifecycle hooks")
	assert.Nil(t, backend.Skills(), "Aider doesn't support skills")
	assert.NotNil(t, backend.Context(), "Aider should support context injection")
	assert.Nil(t, backend.MCP(), "Aider doesn't support MCP servers")
}

// TestNewAider_SupportedModes verifies that Aider supports both execution modes.
func TestNewAider_SupportedModes(t *testing.T) {
	backend := NewAider()
	modes := backend.SupportedModes()

	assert.Len(t, modes, 2)
	assert.Contains(t, modes, ModeInteractive)
	assert.Contains(t, modes, ModeOneshot)
}

// =============================================================================
// Aider Argument Building Tests
//
// buildArgs constructs command-line arguments for aider.
// =============================================================================

// TestAider_BuildArgs_AutoApprove verifies that auto-approve adds --yes-always.
func TestAider_BuildArgs_AutoApprove(t *testing.T) {
	backend := NewAider()

	req := &ExecuteRequest{
		AutoApprove: true,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--yes-always")
}

// TestAider_BuildArgs_Model verifies that custom model is passed via --model.
func TestAider_BuildArgs_Model(t *testing.T) {
	backend := NewAider()

	req := &ExecuteRequest{
		Model: "gpt-4-turbo",
	}
	args := backend.buildArgs(req)

	found := false
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) && args[i+1] == "gpt-4-turbo" {
			found = true
			break
		}
	}
	assert.True(t, found, "--model flag should be set")
}

// TestAider_BuildArgs_Temperature verifies that temperature is passed correctly.
func TestAider_BuildArgs_Temperature(t *testing.T) {
	backend := NewAider()

	req := &ExecuteRequest{
		Temperature: 0.7,
	}
	args := backend.buildArgs(req)

	found := false
	for i, arg := range args {
		if arg == "--temperature" && i+1 < len(args) {
			found = true
			break
		}
	}
	assert.True(t, found, "--temperature flag should be set")
}

// TestAider_BuildArgs_PromptWithMessage verifies that prompts are passed
// via --message with context prepended if available.
func TestAider_BuildArgs_PromptWithMessage(t *testing.T) {
	backend := NewAider()

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "Review this code"},
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--message")
}

// TestAider_BuildArgs_NoPrompt verifies that missing prompt doesn't add
// the --message flag.
func TestAider_BuildArgs_NoPrompt(t *testing.T) {
	backend := NewAider()

	req := &ExecuteRequest{
		Prompt: nil,
	}
	args := backend.buildArgs(req)

	assert.NotContains(t, args, "--message")
}

// TestAider_BuildArgs_Combined verifies combined options work correctly.
func TestAider_BuildArgs_Combined(t *testing.T) {
	backend := NewAider()
	backend.Args = []string{"--dark-mode"}

	req := &ExecuteRequest{
		AutoApprove: true,
		Model:       "claude-3-opus",
		Temperature: 0.5,
		Prompt:      &Fragment{Content: "Fix bugs"},
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--dark-mode")
	assert.Contains(t, args, "--yes-always")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "--temperature")
	assert.Contains(t, args, "--message")
}

// TestAider_Cleanup_NoError verifies that Cleanup returns nil (no cleanup needed).
func TestAider_Cleanup_NoError(t *testing.T) {
	backend := NewAider()
	err := backend.Cleanup(context.TODO())
	assert.NoError(t, err)
}

// Codex backend tests verify the OpenAI Codex CLI integration.
// Codex uses a different approach than Claude Code - it receives context
// via command line arguments rather than file-based injection.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Codex Capability Tests
// =============================================================================

func TestCodex_Lifecycle_ReturnsNil(t *testing.T) {
	codex := NewCodex()
	assert.Nil(t, codex.Lifecycle())
}

func TestCodex_Skills_ReturnsNil(t *testing.T) {
	codex := NewCodex()
	assert.Nil(t, codex.Skills())
}

func TestCodex_MCP_ReturnsNil(t *testing.T) {
	codex := NewCodex()
	assert.Nil(t, codex.MCP())
}

func TestCodex_Context_ReturnsProvider(t *testing.T) {
	codex := NewCodex()
	assert.NotNil(t, codex.Context())
}

func TestCodex_History_ReturnsAccessor(t *testing.T) {
	codex := NewCodex()
	assert.NotNil(t, codex.History())
}

// =============================================================================
// buildArgs Tests
// =============================================================================

func TestCodex_buildArgs_Basic(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "test prompt"},
	}

	args := codex.buildArgs(req, false)

	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "gpt-4")
	assert.Contains(t, args, "test prompt")
	assert.NotContains(t, args, "--full-auto")
	assert.NotContains(t, args, "--quiet")
}

func TestCodex_buildArgs_AutoApprove(t *testing.T) {
	codex := NewCodex()

	req := &ExecuteRequest{
		AutoApprove: true,
		Prompt:      &Fragment{Content: "test"},
	}

	args := codex.buildArgs(req, false)

	assert.Contains(t, args, "--full-auto")
}

func TestCodex_buildArgs_Quiet(t *testing.T) {
	codex := NewCodex()

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "test"},
	}

	args := codex.buildArgs(req, true)

	assert.Contains(t, args, "--quiet")
}

func TestCodex_buildArgs_WithContext(t *testing.T) {
	codex := NewCodex()

	// Provide context via the context provider
	_ = codex.context.Provide("/tmp", []*Fragment{
		{Content: "Test context content"},
	})

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "user task"},
	}

	args := codex.buildArgs(req, false)

	// The last argument should contain both context and prompt
	lastArg := args[len(args)-1]
	assert.Contains(t, lastArg, "Context:")
	assert.Contains(t, lastArg, "Test context content")
	assert.Contains(t, lastArg, "user task")
}

func TestCodex_buildArgs_EmptyPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: ""},
	}

	args := codex.buildArgs(req, false)

	// Should only have base args when prompt content is empty
	assert.Equal(t, []string{"--model", "gpt-4"}, args)
}

func TestCodex_buildArgs_NilPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}

	req := &ExecuteRequest{
		Prompt: nil,
	}

	args := codex.buildArgs(req, false)

	// Should only have base args when prompt is nil
	assert.Equal(t, []string{"--model", "gpt-4"}, args)
}

func TestCodex_buildArgs_PreservesBaseArgs(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--arg1", "--arg2"}

	req := &ExecuteRequest{
		Prompt: &Fragment{Content: "test"},
	}

	// Build args multiple times to ensure base args aren't modified
	_ = codex.buildArgs(req, false)
	args2 := codex.buildArgs(req, false)

	// Original Args should be at the beginning
	assert.Equal(t, "--arg1", args2[0])
	assert.Equal(t, "--arg2", args2[1])

	// Verify original Args slice wasn't mutated
	assert.Equal(t, []string{"--arg1", "--arg2"}, codex.Args)
}

func TestCodex_buildArgs_AllFlags(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--base"}

	// Provide context
	_ = codex.context.Provide("/tmp", []*Fragment{
		{Content: "context content"},
	})

	req := &ExecuteRequest{
		AutoApprove: true,
		Prompt:      &Fragment{Content: "do something"},
	}

	args := codex.buildArgs(req, true)

	assert.Contains(t, args, "--base")
	assert.Contains(t, args, "--full-auto")
	assert.Contains(t, args, "--quiet")

	// Message should contain context and prompt
	lastArg := args[len(args)-1]
	assert.Contains(t, lastArg, "Context:")
	assert.Contains(t, lastArg, "context content")
	assert.Contains(t, lastArg, "do something")
}

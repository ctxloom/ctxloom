package codex

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Capability wiring — codex is a full LaunchBackend agent.
// =============================================================================

func TestCodex_Capabilities(t *testing.T) {
	codex := NewCodex(nil)
	assert.Equal(t, "codex", codex.Name())
	assert.NotNil(t, codex.History(), "session history")
}

func TestCodex_Configure(t *testing.T) {
	codex := NewCodex(nil)
	codex.Configure(&CodexConfig{
		BinaryPath: "/custom/codex",
		Args:       []string{"--foo"},
		Env:        map[string]string{"K": "V"},
	})
	assert.Equal(t, "/custom/codex", codex.BinaryPath)
	assert.Equal(t, []string{"--foo"}, codex.Args)
	assert.Equal(t, "V", codex.Env["K"])
}

// =============================================================================
// buildArgs — context now reaches codex via the SessionStart hook + context
// file, so buildArgs no longer prepends context to the prompt.
// =============================================================================

func TestCodex_buildArgs_InteractiveBasic(t *testing.T) {
	codex := NewCodex(nil)
	codex.Args = []string{"--model", "gpt-4"}

	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test prompt"}}
	args := codex.buildArgs(req)

	assert.NotContains(t, args, "exec", "interactive runs do not use the exec subcommand")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "gpt-4")
	assert.Contains(t, args, "test prompt")
	assert.NotContains(t, args, "--full-auto")
}

func TestCodex_buildArgs_OneshotUsesExec(t *testing.T) {
	codex := NewCodex(nil)
	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "summarize"}}
	args := codex.buildArgs(req)

	assert.Equal(t, "exec", args[0], "oneshot runs use the non-interactive exec subcommand")
	assert.Equal(t, "summarize", args[len(args)-1])
}

func TestCodex_buildArgs_SkipSetupIsolated(t *testing.T) {
	codex := NewCodex(nil)
	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Prompt: &agent.Fragment{Content: "distill"}}
	args := codex.buildArgs(req)

	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "exec")
	assert.Contains(t, joined, "--sandbox read-only", "distill runs read-only")
	assert.Contains(t, joined, "--ask-for-approval never", "distill never prompts for approval")
	assert.NotContains(t, args, "--full-auto", "SkipSetup takes precedence over auto-approve")
}

func TestCodex_buildArgs_AutoApprove(t *testing.T) {
	codex := NewCodex(nil)
	req := &agent.ExecuteRequest{AutoApprove: true, Prompt: &agent.Fragment{Content: "test"}}
	assert.Contains(t, codex.buildArgs(req), "--full-auto")
}

func TestCodex_buildArgs_EmptyPrompt(t *testing.T) {
	codex := NewCodex(nil)
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: ""}}
	assert.Equal(t, []string{"--model", "gpt-4"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_NilPrompt(t *testing.T) {
	codex := NewCodex(nil)
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: nil}
	assert.Equal(t, []string{"--model", "gpt-4"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_PreservesBaseArgs(t *testing.T) {
	codex := NewCodex(nil)
	codex.Args = []string{"--arg1", "--arg2"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test"}}

	_ = codex.buildArgs(req)
	args2 := codex.buildArgs(req)

	assert.Equal(t, "--arg1", args2[0])
	assert.Equal(t, "--arg2", args2[1])
	assert.Equal(t, []string{"--arg1", "--arg2"}, codex.Args, "base Args must not be mutated")
}

func TestCodex_buildArgs_Model(t *testing.T) {
	codex := NewCodex(nil)
	req := &agent.ExecuteRequest{Model: "gpt-5-codex", Prompt: &agent.Fragment{Content: "x"}}
	args := codex.buildArgs(req)

	found := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "gpt-5-codex" {
			found = true
		}
	}
	assert.True(t, found, "buildArgs passes --model <model> when requested")
}

func TestCodex_buildArgs_NoModelWhenEmpty(t *testing.T) {
	codex := NewCodex(nil)
	req := &agent.ExecuteRequest{Prompt: &agent.Fragment{Content: "x"}}
	assert.NotContains(t, codex.buildArgs(req), "--model", "no --model when none requested (codex uses its default)")
}

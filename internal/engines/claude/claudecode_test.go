package claude

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// ClaudeCode Backend Construction Tests
//
// Claude Code is the primary backend for Anthropic's Claude CLI. These tests
// verify proper initialization and configuration of the backend.
// =============================================================================

// TestNewClaudeCode_DefaultValues verifies that a new Claude Code backend
// is created with sensible defaults for binary path and capabilities.
func TestNewClaudeCode_DefaultValues(t *testing.T) {
	backend := NewClaudeCode()

	assert.Equal(t, "claude-code", backend.Name())
	assert.Equal(t, "1.0.0", backend.Version())
	assert.Equal(t, "claude", backend.BinaryPath)
	assert.Empty(t, backend.Args)
}

// TestNewClaudeCode_SupportedModes verifies that Claude Code supports both
// interactive and oneshot execution modes.
func TestNewClaudeCode_SupportedModes(t *testing.T) {
	backend := NewClaudeCode()
	modes := backend.SupportedModes()

	assert.Len(t, modes, 2)
	assert.Contains(t, modes, agent.ModeInteractive)
	assert.Contains(t, modes, agent.ModeOneshot)
}

// =============================================================================
// ClaudeCode Configuration Tests
//
// Configure applies user plugin settings to customize the backend behavior.
// =============================================================================

// TestClaudeCode_Configure_BinaryPath verifies that custom binary paths
// override the default "claude" command.
func TestClaudeCode_Configure_BinaryPath(t *testing.T) {
	backend := NewClaudeCode()

	cfg := &ClaudeConfig{
		BinaryPath: "/custom/path/to/claude",
	}
	backend.Configure(cfg)

	assert.Equal(t, "/custom/path/to/claude", backend.BinaryPath)
}

// TestClaudeCode_Configure_Args verifies that custom arguments are applied
// to the backend configuration.
func TestClaudeCode_Configure_Args(t *testing.T) {
	backend := NewClaudeCode()

	cfg := &ClaudeConfig{
		Args: []string{"--no-telemetry", "--config", "/custom/config"},
	}
	backend.Configure(cfg)

	assert.Equal(t, []string{"--no-telemetry", "--config", "/custom/config"}, backend.Args)
}

// TestClaudeCode_Configure_RequiresNonNil documents that Configure expects
// a non-nil config. Callers should check for nil before calling Configure.
// ApplyLLMConfig in registry.go handles the nil check.
func TestClaudeCode_Configure_RequiresNonNil(t *testing.T) {
	backend := NewClaudeCode()

	// Configure with empty config (not nil) should work
	cfg := &ClaudeConfig{}
	backend.Configure(cfg)

	// Defaults should be preserved
	assert.Equal(t, "claude", backend.BinaryPath)
}

// TestClaudeCode_Configure_EmptyFields verifies that empty config fields
// preserve existing values rather than clearing them.
func TestClaudeCode_Configure_EmptyFields(t *testing.T) {
	backend := NewClaudeCode()

	cfg := &ClaudeConfig{
		// BinaryPath, Args, Env all empty
	}
	backend.Configure(cfg)

	// Original default should be preserved
	assert.Equal(t, "claude", backend.BinaryPath)
}

// =============================================================================
// ClaudeCode Argument Building Tests
//
// buildArgs constructs the command-line arguments for the claude command.
// =============================================================================

// TestClaudeCode_BuildArgs_AutoApprove verifies that auto-approve mode
// adds the --dangerously-skip-permissions flag for non-interactive use.
func TestClaudeCode_BuildArgs_AutoApprove(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Permissions: agent.PermissionBypass,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--dangerously-skip-permissions")
}

// TestClaudeCode_BuildArgs_PermissionModes verifies the non-bypass postures map
// to --permission-mode, and the default posture adds no permission flag at all.
func TestClaudeCode_BuildArgs_PermissionModes(t *testing.T) {
	backend := NewClaudeCode()
	cases := []struct {
		perm     agent.PermissionMode
		wantFlag string // "" = no --permission-mode flag
	}{
		{agent.PermissionDefault, ""},
		{agent.PermissionAcceptEdits, "acceptEdits"},
		{agent.PermissionPlan, "plan"},
	}
	for _, tc := range cases {
		t.Run(tc.perm.String(), func(t *testing.T) {
			args := backend.buildArgs(&agent.ExecuteRequest{Permissions: tc.perm})
			assert.NotContains(t, args, "--dangerously-skip-permissions")
			if tc.wantFlag == "" {
				assert.NotContains(t, args, "--permission-mode")
			} else {
				assert.Subset(t, args, []string{"--permission-mode", tc.wantFlag})
			}
		})
	}
}

// TestClaudeCode_BuildArgs_PlanAddsConservativeDisallowedTools pins that
// plan gets a belt-and-suspenders --disallowedTools on top of
// --permission-mode plan (LIVE VERIFIED tool-name vocabulary, see the
// buildArgs comment), while every other posture is untouched by it.
func TestClaudeCode_BuildArgs_PlanAddsConservativeDisallowedTools(t *testing.T) {
	backend := NewClaudeCode()
	args := backend.buildArgs(&agent.ExecuteRequest{Permissions: agent.PermissionPlan})
	assert.Subset(t, args, []string{"--disallowedTools", "Bash,Edit,Write,NotebookEdit"})

	for _, perm := range []agent.PermissionMode{agent.PermissionDefault, agent.PermissionAcceptEdits, agent.PermissionBypass} {
		t.Run(perm.String(), func(t *testing.T) {
			args := backend.buildArgs(&agent.ExecuteRequest{Permissions: perm})
			assert.NotContains(t, args, "--disallowedTools")
		})
	}
}

// TestClaudeCode_BuildArgs_Model verifies that a custom model is passed
// via the --model flag.
func TestClaudeCode_BuildArgs_Model(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Model: "claude-3-sonnet",
	}
	args := backend.buildArgs(req)

	found := false
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) && args[i+1] == "claude-3-sonnet" {
			found = true
			break
		}
	}
	assert.True(t, found, "--model flag should be set")
}

// TestClaudeCode_BuildArgs_OneshotMode verifies that oneshot mode adds
// the --print flag for single-response execution.
func TestClaudeCode_BuildArgs_OneshotMode(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode: agent.ModeOneshot,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--print")
}

// TestClaudeCode_BuildArgs_OneshotPromptOffArgv verifies the oneshot task is NOT
// baked into argv (it rides stdin via promptStdin, so a large task can't exceed
// the OS argv length limit — the E2BIG that broke `ctxloom weave` synthesis),
// while an interactive run still passes its initial prompt as a positional arg.
func TestClaudeCode_BuildArgs_OneshotPromptOffArgv(t *testing.T) {
	backend := NewClaudeCode()
	const task = "review this enormous diff please"
	prompt := &agent.Fragment{Content: task}

	oneshot := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: prompt})
	assert.NotContains(t, oneshot, task, "oneshot must keep the task off argv (it rides stdin)")
	require.NotNil(t, promptStdin(&agent.ExecuteRequest{Prompt: prompt}), "promptStdin must carry the task")

	interactive := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: prompt})
	assert.Contains(t, interactive, task, "interactive still passes the initial prompt as a positional arg")
}

// TestClaudeCode_PromptStdin_NilWhenNoPrompt verifies an empty prompt yields no
// stdin reader, so a no-prompt oneshot leaves the child's stdin nil (null device)
// rather than an empty pipe.
func TestClaudeCode_PromptStdin_NilWhenNoPrompt(t *testing.T) {
	assert.Nil(t, promptStdin(&agent.ExecuteRequest{}), "no prompt → nil stdin")
}

// TestClaudeCode_BuildArgs_OrdinaryOneshotNoJSON verifies that an ordinary
// oneshot (e.g. `ctxloom run --print`) keeps streaming text output and does not
// switch to the JSON envelope. The backend here ran no minimal Setup, which is
// what "ordinary" now means.
func TestClaudeCode_BuildArgs_OrdinaryOneshotNoJSON(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode: agent.ModeOneshot,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--print")
	assert.NotContains(t, args, "--output-format")
}

// TestClaudeCode_BuildArgs_InteractiveMode verifies that interactive mode
// does not add the --print flag.
func TestClaudeCode_BuildArgs_InteractiveMode(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode: agent.ModeInteractive,
	}
	args := backend.buildArgs(req)

	assert.NotContains(t, args, "--print")
}

// TestClaudeCode_BuildArgs_InteractiveNamesSession verifies that an interactive
// session is named after ctxloom's harp via --name so claude's prompt box,
// /resume picker, and terminal title match the session identity.
func TestClaudeCode_BuildArgs_InteractiveNamesSession(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode: agent.ModeInteractive,
		Env:  map[string]string{sessionHarpEnv: "fair-pushy-cable"},
	}
	args := backend.buildArgs(req)

	assert.True(t, argPair(args, "--name", "fair-pushy-cable"),
		"interactive session should be named after the harp")
}

// TestClaudeCode_BuildArgs_NoHarpNoName verifies that with no harp in env the
// session is left unnamed rather than passing an empty --name.
func TestClaudeCode_BuildArgs_NoHarpNoName(t *testing.T) {
	backend := NewClaudeCode()

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive})

	assert.NotContains(t, args, "--name",
		"absent harp must not produce a --name flag")
}

// TestClaudeCode_BuildArgs_Prompt verifies that prompt content is appended
// as the final argument.
func TestClaudeCode_BuildArgs_Prompt(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Prompt: &agent.Fragment{Content: "Review this code"},
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "Review this code")
}

// TestClaudeCode_BuildArgs_NoPrompt verifies that missing prompt doesn't
// add empty arguments.
func TestClaudeCode_BuildArgs_NoPrompt(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Prompt: nil,
	}
	args := backend.buildArgs(req)

	// Should not contain empty string
	for _, arg := range args {
		assert.NotEmpty(t, arg, "Should not have empty argument")
	}
}

// TestClaudeCode_BuildArgs_Combined verifies that multiple options are
// combined correctly into the argument list.
func TestClaudeCode_BuildArgs_Combined(t *testing.T) {
	backend := NewClaudeCode()
	backend.Args = []string{"--existing-arg"}

	req := &agent.ExecuteRequest{
		Permissions: agent.PermissionBypass,
		Model:       "opus",
		Mode:        agent.ModeOneshot,
		Prompt:      &agent.Fragment{Content: "Test prompt"},
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--existing-arg")
	assert.Contains(t, args, "--dangerously-skip-permissions")
	assert.Contains(t, args, "--print")
	// The oneshot task is delivered on stdin, not argv (see promptStdin), so a
	// large prompt can't blow the argv length limit.
	assert.NotContains(t, args, "Test prompt")
	require.NotNil(t, promptStdin(req))
}

// --- the prompt positional must survive a variadic flag ---------------------

// LIVE REPRO (claude 2.1.220):
//
//	claude -p --tools "" --disallowedTools "Bash,Edit" "Reply with exactly: PROMPTOK"
//	  -> Permission deny rule "Reply" matches no known tool  (x4)
//	     Error: Input must be provided...
//
// claude --help declares --disallowedTools, --tools and --mcp-config as
// VARIADIC (`<tools...>`), so any one of them landing last before the prompt
// positional makes the parser swallow the prompt as flag values: the run
// starts with no task, or with a mangled one, and nothing says so. Today's
// safety is incidental — it holds only while --model or --name happens to
// follow.
//
// The reachable argv: PermissionPlan + ModeInteractive + empty Model + no
// harp + an isolated cell (which skips the surface flags entirely).
func TestClaudeCode_BuildArgs_PromptIsTerminated(t *testing.T) {
	backend := NewClaudeCode()
	const task = "Reply with exactly: PROMPTOK"

	args := backend.buildArgs(&agent.ExecuteRequest{
		Mode:        agent.ModeInteractive,
		Permissions: agent.PermissionPlan,
		Prompt:      &agent.Fragment{Content: task},
		CellKind:    agent.CellKindDirectoryIsolated,
	})

	require.GreaterOrEqual(t, len(args), 2)
	assert.Equal(t, task, args[len(args)-1], "the prompt is the trailing positional")
	assert.Equal(t, "--", args[len(args)-2],
		"a variadic flag immediately before the prompt swallows it; -- is what stops the parser")
}

// The terminator must NOT appear when there is no positional to protect —
// an argv ending in a bare `--` is noise, and claude has no other positional.
func TestClaudeCode_BuildArgs_NoTerminatorWithoutPrompt(t *testing.T) {
	backend := NewClaudeCode()

	for _, req := range []*agent.ExecuteRequest{
		{Mode: agent.ModeInteractive},
		{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "off argv"}},
	} {
		assert.NotContains(t, backend.buildArgs(req), "--",
			"no prompt positional means no terminator")
	}
}

// Anti-drift: whatever else buildArgs learns to emit, EVERY interactive shape
// that carries a prompt must terminate it. This is the assertion that makes
// the fix survive a future flag being appended after the prompt block, which
// is precisely how the original defect became reachable.
func TestClaudeCode_BuildArgs_EveryPromptShapeIsTerminated(t *testing.T) {
	backend := NewClaudeCode()
	const task = "do the thing"

	for _, perm := range []agent.PermissionMode{
		agent.PermissionDefault, agent.PermissionBypass,
		agent.PermissionAcceptEdits, agent.PermissionPlan,
	} {
		for _, model := range []string{"", "sonnet"} {
			for _, cell := range []agent.CellKind{agent.CellKindDirectoryIsolated, agent.CellKindShared} {
				args := backend.buildArgs(&agent.ExecuteRequest{
					Mode:        agent.ModeInteractive,
					Permissions: perm,
					Model:       model,
					CellKind:    cell,
					Prompt:      &agent.Fragment{Content: task},
				})
				if !assert.Equal(t, task, args[len(args)-1]) {
					continue
				}
				if !assert.GreaterOrEqual(t, len(args), 2) {
					continue
				}
				assert.Equal(t, "--", args[len(args)-2],
					"perm=%v model=%q cell=%v", perm, model, cell)
			}
		}
	}
}

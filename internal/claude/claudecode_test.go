package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// TestClaudeCode_Configure_Env verifies that environment variables are
// merged into the backend's environment.
func TestClaudeCode_Configure_Env(t *testing.T) {
	backend := NewClaudeCode()

	cfg := &ClaudeConfig{
		Env: map[string]string{
			"ANTHROPIC_API_KEY": "test-key",
			"CUSTOM_VAR":        "custom-value",
		},
	}
	backend.Configure(cfg)

	assert.Equal(t, "test-key", backend.Env["ANTHROPIC_API_KEY"])
	assert.Equal(t, "custom-value", backend.Env["CUSTOM_VAR"])
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

// TestNewClaudeCode_ThinkingDefaultsToMedium pins that a freshly-constructed
// backend that never runs Configure at all still resolves to the documented
// medium default (agent.ThinkingLevel's zero value IS ThinkingMedium — see
// thinking.go) — the "fires by default" guarantee must not depend on
// Configure having run.
func TestNewClaudeCode_ThinkingDefaultsToMedium(t *testing.T) {
	backend := NewClaudeCode()
	assert.Equal(t, agent.ThinkingMedium, backend.thinking)
}

// TestClaudeCode_Configure_Thinking verifies the normalized thinking level
// parses through Configure onto the backend.
func TestClaudeCode_Configure_Thinking(t *testing.T) {
	backend := NewClaudeCode()
	backend.Configure(&ClaudeConfig{Thinking: "high"})
	assert.Equal(t, agent.ThinkingHigh, backend.thinking)
}

// TestClaudeCode_Configure_ThinkingEmptyDefaultsToMedium verifies an unset
// Thinking field resolves to the documented medium default.
func TestClaudeCode_Configure_ThinkingEmptyDefaultsToMedium(t *testing.T) {
	backend := NewClaudeCode()
	backend.Configure(&ClaudeConfig{})
	assert.Equal(t, agent.ThinkingMedium, backend.thinking)
}

// TestClaudeCode_Configure_ThinkingUnknownWarnsAndDefaults verifies an
// unrecognized (but non-empty) value still resolves to medium AND warns —
// an honest degrade, not a silent one.
func TestClaudeCode_Configure_ThinkingUnknownWarnsAndDefaults(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w

	backend := NewClaudeCode()
	backend.Configure(&ClaudeConfig{Thinking: "extreme"})
	_ = w.Close()
	os.Stderr = orig

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, agent.ThinkingMedium, backend.thinking)
	assert.Contains(t, string(out), "extreme")
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

// TestClaudeCode_BuildArgs_NativeContextFlag verifies that after Setup a normal
// run loads ctxloom's assembled context via --append-system-prompt-file pointing
// at the framed file the delivery seam materialized, while the minimal/distill
// path (which drops context) does not. This is the SessionStart-injection
// replacement for claude.
func TestClaudeCode_BuildArgs_NativeContextFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep HarpEphemeralDir under a temp home
	backend := NewClaudeCode()
	work := t.TempDir()

	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{sessionHarpEnv: "perky-same-chevy"},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   &agent.ManagedConfig{},
	}))
	framed := backend.surfaces.Context.Path()
	require.NotEmpty(t, framed, "Setup must materialize the framed context file for the flag")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive})
	assert.True(t, argPair(args, "--append-system-prompt-file", framed),
		"a normal run loads ctxloom context via --append-system-prompt-file")

	minArgs := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true})
	assert.NotContains(t, minArgs, "--append-system-prompt-file",
		"minimal/distill mode must not load ctxloom context")
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

// TestClaudeCode_BuildArgs_MinimalOneshotRequestsJSON verifies that minimal
// oneshot mode (distillation/compaction) requests the JSON envelope so Execute
// can read the resolved model id instead of guessing.
func TestClaudeCode_BuildArgs_MinimalOneshotRequestsJSON(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode:      agent.ModeOneshot,
		SkipSetup: true,
	}
	args := backend.buildArgs(req)

	assert.Contains(t, args, "--print")
	assert.True(t, argPair(args, "--output-format", "json"),
		"minimal oneshot should request JSON output")
}

// TestClaudeCode_BuildArgs_MinimalModeNoModelByDefault verifies that minimal
// mode with no explicit model adds no --model flag: the model is resolved by
// the caller from the fast role's labeled config, not defaulted in the backend.
func TestClaudeCode_BuildArgs_MinimalModeNoModelByDefault(t *testing.T) {
	backend := NewClaudeCode()

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true})

	assert.NotContains(t, args, "--model",
		"minimal mode must not default a model; the caller supplies it")
}

// TestClaudeCode_BuildArgs_ExplicitModelWinsInMinimalMode verifies that a
// configured fast model (passed as req.Model) overrides the backend default.
func TestClaudeCode_BuildArgs_ExplicitModelWinsInMinimalMode(t *testing.T) {
	backend := NewClaudeCode()

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Model: "sonnet"})

	assert.True(t, argPair(args, "--model", "sonnet"))
}

// TestClaudeCode_BuildArgs_MinimalModeIsolatesViaSettings verifies the headless
// distill path isolates from CLAUDE.md/memory/MCP via --system-prompt "",
// --strict-mcp-config, and an inline --settings override — NOT --setting-sources
// "" (an empty source list drops the model config and routes generation to the
// CLI's fast model regardless of --model).
func TestClaudeCode_BuildArgs_MinimalModeIsolatesViaSettings(t *testing.T) {
	backend := NewClaudeCode()

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Model: "claude-opus-4-8"})

	assert.NotContains(t, args, "--setting-sources",
		"empty --setting-sources drops model routing; isolate via --settings instead")
	assert.True(t, argPair(args, "--system-prompt", ""),
		"minimal mode drops the built-in system prompt (CLAUDE.md/memory)")
	assert.Contains(t, args, "--strict-mcp-config")
	assert.True(t, argPair(args, "--model", "claude-opus-4-8"))
	// The --settings payload carries the requested model and disables hooks.
	settings := argValue(args, "--settings")
	assert.Contains(t, settings, "claude-opus-4-8")
	assert.Contains(t, settings, "\"hooks\":{}")
}

// TestMinimalSettings verifies the isolation JSON: hooks disabled, model present
// only when supplied.
func TestMinimalSettings(t *testing.T) {
	withModel := minimalSettings("claude-opus-4-8")
	assert.Contains(t, withModel, "\"model\":\"claude-opus-4-8\"")
	assert.Contains(t, withModel, "\"hooks\":{}")

	noModel := minimalSettings("")
	assert.NotContains(t, noModel, "\"model\"", "empty model is omitted so the CLI default applies")
	assert.Contains(t, noModel, "\"hooks\":{}")
}

// TestClaudeCode_BuildArgs_OneshotWithoutSkipSetupNoJSON verifies that an
// ordinary oneshot (e.g. `ctxloom run --print`) keeps streaming text output and
// does not switch to the JSON envelope.
func TestClaudeCode_BuildArgs_OneshotWithoutSkipSetupNoJSON(t *testing.T) {
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

// TestClaudeCode_BuildArgs_MinimalModeNoName verifies that throwaway minimal
// oneshot runs are not named even when a harp is present in env.
func TestClaudeCode_BuildArgs_MinimalModeNoName(t *testing.T) {
	backend := NewClaudeCode()

	req := &agent.ExecuteRequest{
		Mode:      agent.ModeOneshot,
		SkipSetup: true,
		Env:       map[string]string{sessionHarpEnv: "fair-pushy-cable"},
	}
	args := backend.buildArgs(req)

	assert.NotContains(t, args, "--name",
		"throwaway oneshot runs must not be named")
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

// =============================================================================
// JSON Envelope Parsing
//
// In minimal oneshot mode the Claude CLI is invoked with --output-format json.
// parseClaudeJSONResult extracts the assistant text and the resolved model id
// (claude reports actual ids under modelUsage) so distilled_by records the real
// model rather than a hardcoded guess.
// =============================================================================

// TestParseClaudeJSONResult_ExtractsResultAndModel verifies that a well-formed
// envelope yields both the result text and the resolved model id.
func TestParseClaudeJSONResult_ExtractsResultAndModel(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","is_error":false,` +
		`"result":"# Distilled\n- point one","session_id":"abc",` +
		`"modelUsage":{"claude-haiku-4-5":{"inputTokens":500,"outputTokens":16}}}`

	text, model, err := parseClaudeJSONResult([]byte(envelope))

	assert.NoError(t, err)
	assert.Equal(t, "# Distilled\n- point one", text)
	assert.Equal(t, "claude-haiku-4-5", model)
}

// TestParseClaudeJSONResult_PicksWorkingModel verifies that when the CLI reports
// several models, provenance records the model that GENERATED the result — the
// one with the most output tokens — not a fast helper the CLI routes a large
// read through (high input, tiny output). Mirrors observed distill envelopes
// where a helper reads the payload and the requested model does the generation.
func TestParseClaudeJSONResult_PicksWorkingModel(t *testing.T) {
	envelope := `{"result":"ok","modelUsage":{` +
		`"claude-haiku-4-5":{"inputTokens":500,"outputTokens":16},` +
		`"claude-opus-4-8":{"inputTokens":1,"outputTokens":54}}}`

	_, model, err := parseClaudeJSONResult([]byte(envelope))

	assert.NoError(t, err)
	assert.Equal(t, "claude-opus-4-8", model)
}

// TestParseClaudeJSONResult_InvalidJSON verifies that malformed output is
// reported as an error so callers can fall back to the raw bytes rather than
// fabricating a result.
func TestParseClaudeJSONResult_InvalidJSON(t *testing.T) {
	_, _, err := parseClaudeJSONResult([]byte("not json at all"))

	assert.Error(t, err)
}

// The minimal-oneshot branch — the distill/compaction path — could
// return ExitCode 0 with a nil error having written ZERO bytes to stdout, two
// ways: the CLI exits 0 emitting nothing (the envelope fails to parse, the
// empty buffer is copied through, and the nil error is returned), or the
// envelope parses fine with an empty "result". An exit-code gate sees neither,
// so a distill that produced nothing reads exactly like one that worked.
func TestClaudeCode_MinimalOneshot_NoOutput_IsAnError(t *testing.T) {
	for _, tc := range []struct {
		name  string
		emit  string
		wants string
	}{
		{"CLI emitted nothing at all", "", "no output"},
		{"envelope parsed with an empty result", `{"result":"","modelUsage":{}}`, "no output"},
		{"envelope parsed with only whitespace", `{"result":"   \n","modelUsage":{}}`, "no output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewClaudeCode()
			b.SetLauncher(func(_ context.Context, _ agent.LaunchSpec, _ io.Reader, stdout, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
				_, _ = io.WriteString(stdout, tc.emit)
				return 0, nil
			})

			var out, errBuf bytes.Buffer
			res, err := b.Execute(context.Background(), &agent.ExecuteRequest{
				Mode:      agent.ModeOneshot,
				SkipSetup: true,
				Prompt:    &agent.Fragment{Content: "summarize this"},
			}, &out, &errBuf)

			require.Error(t, err, "a oneshot that produced no output must not report success")
			assert.Contains(t, err.Error(), tc.wants)
			if res != nil {
				assert.NotEqual(t, int32(0), res.ExitCode, "and it must not exit 0")
			}
		})
	}
}

// Real output must still succeed, and must still reach stdout — the guard
// must not turn a working distill into a failure.
func TestClaudeCode_MinimalOneshot_WithOutput_StillSucceeds(t *testing.T) {
	b := NewClaudeCode()
	b.SetLauncher(func(_ context.Context, _ agent.LaunchSpec, _ io.Reader, stdout, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		_, _ = io.WriteString(stdout, `{"result":"the summary","modelUsage":{"claude-x":{"outputTokens":5}}}`)
		return 0, nil
	})

	var out, errBuf bytes.Buffer
	res, err := b.Execute(context.Background(), &agent.ExecuteRequest{
		Mode: agent.ModeOneshot, SkipSetup: true, Prompt: &agent.Fragment{Content: "summarize this"},
	}, &out, &errBuf)

	require.NoError(t, err)
	assert.Equal(t, "the summary", out.String())
	require.NotNil(t, res)
	assert.Equal(t, int32(0), res.ExitCode)
	assert.Equal(t, "claude-x", res.ModelInfo.ModelName)
}

// A NON-ZERO exit with no output keeps the CLI's own exit code and error —
// the guard must not mask a real failure with a synthesized one.
func TestClaudeCode_MinimalOneshot_FailedRun_KeepsItsOwnExitCode(t *testing.T) {
	b := NewClaudeCode()
	b.SetLauncher(func(_ context.Context, _ agent.LaunchSpec, _ io.Reader, _, stderr io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		_, _ = io.WriteString(stderr, "boom")
		return 3, nil
	})

	var out, errBuf bytes.Buffer
	res, err := b.Execute(context.Background(), &agent.ExecuteRequest{
		Mode: agent.ModeOneshot, SkipSetup: true, Prompt: &agent.Fragment{Content: "summarize this"},
	}, &out, &errBuf)

	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, int32(3), res.ExitCode, "the CLI's own exit code must survive")
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
		{Mode: agent.ModeOneshot, SkipSetup: true},
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

// TestMinimalSettings_NeverDegradesToEmptyObject is the pin behind a past
// review's refutation. That review read minimalSettings's `return "{}"` as a
// swallowed marshal error that drops permissions.defaultMode:
// bypassPermissions, which would leave a headless distill run blocking on a
// permission prompt nobody can answer. The branch cannot be reached: the
// value handed to json.Marshal is a fixed graph of string / bool / int /
// []string / map[string]any — no channel, func, NaN/Inf float, or cycle, the
// only things encoding/json refuses — and the one caller-supplied part is a
// plain string, which never fails to marshal (invalid UTF-8 is replaced with
// U+FFFD, not rejected).
//
// So this asserts the PAYLOAD for adversarial model strings instead: valid JSON
// carrying the bypass. It is the pin that bites the change which WOULD make
// that review's claim true — adding an unmarshalable value to that map —
// because the bypass would then be missing from the output.
func TestMinimalSettings_NeverDegradesToEmptyObject(t *testing.T) {
	for _, model := range []string{
		"",
		"claude-opus-4-8",
		"\xff\xfe not utf-8",
		`quote"and}brace`,
		"\x00control",
	} {
		out := minimalSettings(model)
		require.NotEqual(t, "{}", out, "model %q must not degrade the isolation settings to an empty object", model)

		var decoded struct {
			Permissions struct {
				DefaultMode string `json:"defaultMode"`
			} `json:"permissions"`
			Hooks *map[string]any `json:"hooks"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &decoded), "minimalSettings must always emit valid JSON")
		assert.Equal(t, "bypassPermissions", decoded.Permissions.DefaultMode,
			"a headless distill run has nobody to answer a permission prompt")
		require.NotNil(t, decoded.Hooks, "hooks must be overridden, not left to the user's settings")
	}
}

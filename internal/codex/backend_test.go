package codex

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Capability wiring — codex is a full LaunchBackend agent.
// =============================================================================

func TestCodex_Capabilities(t *testing.T) {
	codex := NewCodex()
	assert.Equal(t, "codex", codex.Name())
	assert.NotNil(t, codex.History(), "session history")
}

func TestCodex_Configure(t *testing.T) {
	codex := NewCodex()
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
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}

	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test prompt"}}
	args := codex.buildArgs(req)

	assert.NotContains(t, args, "exec", "interactive runs do not use the exec subcommand")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "gpt-4")
	assert.Contains(t, args, "test prompt")
}

func TestCodex_buildArgs_OneshotUsesExec(t *testing.T) {
	codex := NewCodex()
	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "summarize"}}
	args := codex.buildArgs(req)

	assert.Equal(t, "exec", args[0], "oneshot runs use the non-interactive exec subcommand")
	assert.Equal(t, "summarize", args[len(args)-1])
}

// TestCodex_buildArgs_PostureMatrix pins the argv codex actually receives for every
// posture on BOTH subcommands. The two subcommands take DIFFERENT flags and reject
// a foreign one with exit 2 ("unexpected argument"), so each posture is asserted
// per-mode: --ask-for-approval is interactive-only, and --full-auto exists on
// neither (verified against codex-cli 0.144.4).
func TestCodex_buildArgs_PostureMatrix(t *testing.T) {
	tests := []struct {
		name     string
		req      agent.ExecuteRequest
		wants    []string
		notWants []string
	}{
		{
			name:     "default oneshot states workspace-write",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionDefault},
			wants:    []string{"exec", "--sandbox", "workspace-write"},
			notWants: []string{"--ask-for-approval", "--full-auto", "read-only"},
		},
		{
			name:     "default interactive states workspace-write",
			req:      agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionDefault},
			wants:    []string{"--sandbox", "workspace-write"},
			notWants: []string{"exec", "--ask-for-approval", "--full-auto"},
		},
		{
			name:     "acceptEdits has no codex tier of its own and follows default",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits},
			wants:    []string{"--sandbox", "workspace-write"},
			notWants: []string{"--ask-for-approval", "--full-auto"},
		},
		{
			name:  "plan oneshot is read-only and never names the interactive-only approval flag",
			req:   agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan},
			wants: []string{"exec", "--sandbox", "read-only"},
			// `codex exec --ask-for-approval` is an exit-2 parse error: it kills the run.
			notWants: []string{"--ask-for-approval", "--full-auto", "workspace-write"},
		},
		{
			name:     "plan interactive is read-only and never prompts",
			req:      agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionPlan},
			wants:    []string{"--sandbox", "read-only", "--ask-for-approval", "never"},
			notWants: []string{"exec", "--full-auto", "workspace-write"},
		},
		{
			name:     "bypass oneshot uses codex's full-access escape hatch",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass},
			wants:    []string{"exec", "--dangerously-bypass-approvals-and-sandbox"},
			notWants: []string{"--full-auto", "--sandbox", "--ask-for-approval"},
		},
		{
			name: "bypass interactive uses codex's full-access escape hatch",
			req:  agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionBypass},
			// `codex --full-auto` is an exit-2 parse error: it kills the most common posture.
			wants:    []string{"--dangerously-bypass-approvals-and-sandbox"},
			notWants: []string{"exec", "--full-auto", "--sandbox", "--ask-for-approval"},
		},
		{
			name:     "SkipSetup oneshot (distill/compaction) is read-only, no approval flag",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true},
			wants:    []string{"exec", "--sandbox", "read-only"},
			notWants: []string{"--ask-for-approval", "--full-auto", "workspace-write"},
		},
		{
			name:     "SkipSetup outranks a bypass posture",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Permissions: agent.PermissionBypass},
			wants:    []string{"--sandbox", "read-only"},
			notWants: []string{"--dangerously-bypass-approvals-and-sandbox", "--full-auto"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Prompt = &agent.Fragment{Content: "x"}
			args := NewCodex().buildArgs(&req)

			assert.Subset(t, args, tc.wants, "argv %v", args)
			for _, flag := range tc.notWants {
				assert.NotContains(t, args, flag, "argv %v", args)
			}
			assert.Equal(t, "x", args[len(args)-1], "prompt is the trailing positional")
		})
	}
}

func TestCodex_buildArgs_EmptyPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: ""}}
	assert.Equal(t, []string{"--model", "gpt-4", "--sandbox", "workspace-write"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_NilPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: nil}
	assert.Equal(t, []string{"--model", "gpt-4", "--sandbox", "workspace-write"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_PreservesBaseArgs(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--arg1", "--arg2"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test"}}

	_ = codex.buildArgs(req)
	args2 := codex.buildArgs(req)

	assert.Equal(t, "--arg1", args2[0])
	assert.Equal(t, "--arg2", args2[1])
	assert.Equal(t, []string{"--arg1", "--arg2"}, codex.Args, "base Args must not be mutated")
}

func TestCodex_buildArgs_Model(t *testing.T) {
	codex := NewCodex()
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
	codex := NewCodex()
	req := &agent.ExecuteRequest{Prompt: &agent.Fragment{Content: "x"}}
	assert.NotContains(t, codex.buildArgs(req), "--model", "no --model when none requested (codex uses its default)")
}

// =============================================================================
// CODEX_HOME single ownership (white-dawn §2.2A) — resolveCodexProjectDir is
// the one function Setup's delivery target and Execute's env both read, so
// they can never disagree about where CODEX_HOME points. codex's credential
// seeding is now internal/lm/isolation's copy-based framework
// (credentialSeedSpecs["codex"], balmy-comic) — linkUserCodexAuth's symlink
// is deleted; there is no codex-package credential test left here.
// =============================================================================

// TestResolveCodexProjectDir_NoIsolation_FallsBackToWorkDir pins today's
// default (None/shared-cwd, or no backend context): no isolation-provided
// CODEX_HOME in env → the virtual project dir is WorkDir itself, in-tree,
// exactly as before this fix — isolated=false (no trust pre-seed).
func TestResolveCodexProjectDir_NoIsolation_FallsBackToWorkDir(t *testing.T) {
	dir, isolated := resolveCodexProjectDir(nil, "/proj")
	assert.Equal(t, "/proj", dir)
	assert.False(t, isolated)
}

// TestResolveCodexProjectDir_EmptyWorkDir mirrors cellCodexHomeEnv's old
// "" → "." fallback.
func TestResolveCodexProjectDir_EmptyWorkDir(t *testing.T) {
	dir, isolated := resolveCodexProjectDir(nil, "")
	assert.Equal(t, ".", dir)
	assert.False(t, isolated)
}

// TestResolveCodexProjectDir_IsolationProvided_StripsCodexSuffix is the
// single-owner fix's core case: an isolation-provided CODEX_HOME (worktree's
// per-agent config-home, always ending in "/.codex" — credentialSeedSpecs's
// codex HomeVar Subdir) wins over WorkDir, and the ".codex" suffix is
// stripped back to the virtual project dir cellScopedCodexHome expects — so
// existing writers (SettingsPath, cellScopedPromptsDir/SkillsDir) resolve
// the SAME final home unchanged.
func TestResolveCodexProjectDir_IsolationProvided_StripsCodexSuffix(t *testing.T) {
	dir, isolated := resolveCodexProjectDir(map[string]string{"CODEX_HOME": "/tmp/ctxloom-cfg-x/.codex"}, "/proj")
	assert.Equal(t, "/tmp/ctxloom-cfg-x", dir)
	assert.True(t, isolated, "an isolation-provided home is ephemeral — safe to pre-seed trust into")
	assert.Equal(t, "/tmp/ctxloom-cfg-x/.codex", cellScopedCodexHome(dir), "the final CODEX_HOME round-trips exactly")
}

// TestResolveCodexProjectDir_IsolationProvided_UnexpectedShape covers an
// isolation-provided CODEX_HOME that does NOT end in "/.codex" (a caller
// override) — used AS the project dir directly rather than dropped, so
// Setup and Execute still agree even on an unexpected shape.
func TestResolveCodexProjectDir_IsolationProvided_UnexpectedShape(t *testing.T) {
	dir, isolated := resolveCodexProjectDir(map[string]string{"CODEX_HOME": "/custom/home"}, "/proj")
	assert.Equal(t, "/custom/home", dir)
	assert.True(t, isolated)
}

// TestCodex_SetupExecute_AgreeOnIsolatedCodexHome is the end-to-end PAYLOAD
// test for the precedence bug: Setup (delivery) and cellCodexHomeEnv
// (Execute's env) must resolve to the IDENTICAL CODEX_HOME when isolation
// provides one — this is the assertion that would have caught the original
// bug (the isolation-provided value being silently overridden by the
// backend's own <WorkDir>/.codex).
func TestCodex_SetupExecute_AgreeOnIsolatedCodexHome(t *testing.T) {
	b := NewCodex()
	isolatedHome := filepath.Join(t.TempDir(), ".codex")
	setupReq := &agent.SetupRequest{
		WorkDir: "/proj",
		Env:     map[string]string{"CODEX_HOME": isolatedHome},
	}
	// Setup best-effort delivers files (may warn on I/O in a bare temp tree);
	// what matters here is the resolved state it stashes, not delivery success.
	_ = b.Setup(context.Background(), setupReq)
	assert.Equal(t, filepath.Dir(isolatedHome), b.resolvedProjectDir)
	assert.NotEmpty(t, b.resolvedTrustAbsPath, "an isolation-provided home is trusted-pre-seed eligible")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj", Env: setupReq.Env})
	assert.Equal(t, isolatedHome, execEnv["CODEX_HOME"], "Execute's CODEX_HOME matches exactly what Setup delivered into")
}

// TestCodex_SetupExecute_NoneCellUnchanged pins the deliberately-scoped
// residual: with NO isolation-provided CODEX_HOME (None/shared-cwd), Setup
// and Execute both still land on <WorkDir>/.codex — today's behavior,
// unchanged by this fix.
func TestCodex_SetupExecute_NoneCellUnchanged(t *testing.T) {
	b := NewCodex()
	setupReq := &agent.SetupRequest{WorkDir: "/proj"}
	_ = b.Setup(context.Background(), setupReq)
	assert.Equal(t, "/proj", b.resolvedProjectDir)
	assert.Empty(t, b.resolvedTrustAbsPath, "in-tree config.toml is never trust-pre-seeded")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj"})
	assert.Equal(t, filepath.Join("/proj", ".codex"), execEnv["CODEX_HOME"])
}

// TestCodex_CellCodexHomeEnv_SkipsSetup: SkipSetup (minimal/distill) sets no
// CODEX_HOME at all — codex keeps its global home, matching pre-fix behavior.
func TestCodex_CellCodexHomeEnv_SkipsSetup(t *testing.T) {
	b := NewCodex()
	assert.Nil(t, b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj", SkipSetup: true}))
}

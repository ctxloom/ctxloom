package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
// Credentials — a cell-scoped CODEX_HOME moves codex's auth.json lookup with it.
// =============================================================================

func TestCodex_linkUserCodexAuth(t *testing.T) {
	userHome := t.TempDir()
	authFile := filepath.Join(userHome, "auth.json")
	require.NoError(t, os.WriteFile(authFile, []byte(`{"tokens":{}}`), 0o600))
	t.Setenv("CODEX_HOME", userHome)

	t.Run("links the user's credentials into a cell home codex would otherwise 401 from", func(t *testing.T) {
		cellHome := filepath.Join(t.TempDir(), ".codex")
		require.NoError(t, linkUserCodexAuth(cellHome))

		link := filepath.Join(cellHome, "auth.json")
		target, err := os.Readlink(link)
		require.NoError(t, err, "the seed is a symlink, never a copy of the credential")
		assert.Equal(t, authFile, target)

		body, err := os.ReadFile(link)
		require.NoError(t, err)
		assert.Equal(t, `{"tokens":{}}`, string(body), "codex reads the user's real credential through it")
	})

	t.Run("re-points a stale link", func(t *testing.T) {
		cellHome := filepath.Join(t.TempDir(), ".codex")
		require.NoError(t, os.MkdirAll(cellHome, 0o755))
		require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "gone.json"), filepath.Join(cellHome, "auth.json")))

		require.NoError(t, linkUserCodexAuth(cellHome))
		target, err := os.Readlink(filepath.Join(cellHome, "auth.json"))
		require.NoError(t, err)
		assert.Equal(t, authFile, target)
	})

	t.Run("leaves a real credential file in the cell alone", func(t *testing.T) {
		cellHome := filepath.Join(t.TempDir(), ".codex")
		require.NoError(t, os.MkdirAll(cellHome, 0o755))
		cellAuth := filepath.Join(cellHome, "auth.json")
		require.NoError(t, os.WriteFile(cellAuth, []byte(`{"cell":true}`), 0o600))

		require.NoError(t, linkUserCodexAuth(cellHome))
		body, err := os.ReadFile(cellAuth)
		require.NoError(t, err)
		assert.Equal(t, `{"cell":true}`, string(body))
	})

	t.Run("no-ops when the cell home is the user's home", func(t *testing.T) {
		require.NoError(t, linkUserCodexAuth(userHome))
		info, err := os.Lstat(authFile)
		require.NoError(t, err)
		assert.Zero(t, info.Mode()&os.ModeSymlink, "the user's own auth.json is never replaced by a link to itself")
	})
}

func TestCodex_linkUserCodexAuth_NoUserCredential(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir()) // a home with no auth.json (env-var auth)
	cellHome := filepath.Join(t.TempDir(), ".codex")

	require.NoError(t, linkUserCodexAuth(cellHome), "nothing to link is not an error")
	_, err := os.Lstat(filepath.Join(cellHome, "auth.json"))
	assert.True(t, os.IsNotExist(err), "no dangling link is planted")
}

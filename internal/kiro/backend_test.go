package kiro

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestKiro_SetupExecute_AgreeOnAgentName is the end-to-end PAYLOAD test for
// U055-F01: a configured `agent:` override used to reach buildArgs'
// `--agent <name>` while Setup's writeAgentConfig kept writing the
// materialized custom-agent JSON under the hardcoded defaultAgentName, both
// as the file's path AND its "name" field — so kiro-cli was told to select an
// agent that was never materialized (or a stale/default one), a broken
// launch. Setup and buildArgs must resolve to the SAME name.
func TestKiro_SetupExecute_AgreeOnAgentName(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{Agent: "myagent"})

	work := t.TempDir()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:  work,
		CellKind: agent.CellKindDirectoryIsolated,
		Managed:  &agent.ManagedConfig{},
	}))

	// buildArgs launches with the configured override.
	args := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "hi"}}, "")
	require.Contains(t, args, "--agent")
	idx := -1
	for i, a := range args {
		if a == "--agent" {
			idx = i
		}
	}
	require.NotEqual(t, -1, idx)
	launchedName := args[idx+1]
	assert.Equal(t, "myagent", launchedName)

	// The file Setup actually materialized must exist AT that exact name, and
	// its own "name" field (what kiro-cli itself uses to identify the agent)
	// must agree too.
	path := filepath.Join(work, ".kiro", "agents", launchedName+".json")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "the agent Setup materialized must be the one buildArgs selects with --agent")

	var decoded struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "myagent", decoded.Name, "the materialized agent's own name field must match what --agent selects")
}

func TestKiroConfig_BackendType(t *testing.T) {
	assert.Equal(t, "kiro", KiroConfig{}.BackendType())
}

func TestNewKiro_Defaults(t *testing.T) {
	b := NewKiro()
	assert.Equal(t, "kiro", b.Name())
	assert.Equal(t, "kiro-cli", b.BinaryPath)
}

func TestKiro_Configure(t *testing.T) {
	b := NewKiro()
	b.Configure(&KiroConfig{
		BinaryPath:  "/opt/kiro-cli",
		Args:        []string{"-v"},
		Env:         map[string]string{"FOO": "bar"},
		Effort:      "high",
		Agent:       "myagent",
		AgentEngine: "v3",
	})
	assert.Equal(t, "/opt/kiro-cli", b.BinaryPath)
	assert.Equal(t, []string{"-v"}, b.Args)
	assert.Equal(t, "bar", b.Env["FOO"])
	assert.Equal(t, "high", b.effort)
	assert.Equal(t, "myagent", b.agentName)
	assert.Equal(t, "v3", b.agentEngine)
}

// TestKiro_ConfigureThinkingIsWarnedNoOp pins the honest-no-op contract: kiro
// has no wired mechanism for the cross-engine normalized thinking level
// (unlike claude/codex — see internal/claude/chat.go, internal/codex/chat.go),
// so an explicit `thinking` setting must WARN rather than silently vanish.
func TestKiro_ConfigureThinkingIsWarnedNoOp(t *testing.T) {
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	b := NewKiro()
	b.Configure(&KiroConfig{Thinking: "high"})
	_ = w.Close()
	os.Stderr = orig

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Contains(t, string(out), "thinking", "an explicit setting must be surfaced, not silently swallowed")
	assert.Contains(t, string(out), "high")
}

func TestKiro_ConfigureIgnoresForeignConfig(t *testing.T) {
	b := NewKiro()
	b.Configure(nil)
	assert.Equal(t, "kiro-cli", b.BinaryPath)
	assert.Equal(t, defaultAgentName, b.agentName)
}

// TestKiro_Execute_RefusesEmptyOneshotPrompt pins the headless-turn invariant
// the shared ACP-shaped path already enforces (agent.RunOneshotTurn: "an empty
// prompt now refuses before the engine is ever invoked"). kiro's exec-style
// oneshot appends `--no-interactive` unconditionally but appends the prompt only
// when non-empty, so a blank prompt launched `kiro-cli chat --no-interactive`
// with NO input positional and nil stdin — a headless turn that asks nothing,
// the exit-0-with-zero-bytes shape. The engine must not be spawned at all.
func TestKiro_Execute_RefusesEmptyOneshotPrompt(t *testing.T) {
	for _, tt := range []struct {
		name   string
		prompt *agent.Fragment
	}{
		{"nil prompt", nil},
		{"empty prompt", &agent.Fragment{}},
		{"whitespace-only prompt", &agent.Fragment{Content: " \n\t "}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			launched := false
			b := NewKiro()
			b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
				launched = true
				return 0, nil
			})

			_, err := b.Execute(context.Background(),
				&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: tt.prompt},
				io.Discard, io.Discard)

			require.Error(t, err, "a oneshot that asks nothing must be refused, not launched")
			assert.False(t, launched, "the engine must never be spawned for an empty oneshot prompt")
		})
	}
}

// TestKiro_Execute_InteractiveEmptyPromptIsLegitimate is the other half: an
// interactive launch with no prompt opens a session for the human to drive, so
// it must keep working (cf. internal/cli/run.go's identical --print-only floor).
func TestKiro_Execute_InteractiveEmptyPromptStillLaunches(t *testing.T) {
	launched := false
	b := NewKiro()
	b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
		launched = true
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeInteractive}, io.Discard, io.Discard)

	require.NoError(t, err)
	assert.True(t, launched, "an interactive session with no opening prompt is legitimate")
}

func TestKiro_BuildArgs(t *testing.T) {
	tests := []struct {
		name      string
		configure *KiroConfig
		req       *agent.ExecuteRequest
		model     string
		want      []string
	}{
		{
			name: "oneshot prompt is headless positional",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "do it"}},
			want: []string{"chat", "--agent", "ctxloom", "--no-interactive", "do it"},
		},
		{
			name: "interactive prompt stays in session",
			req:  &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "do it"}},
			want: []string{"chat", "--agent", "ctxloom", "do it"},
		},
		{
			name:  "model pinned",
			req:   &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}},
			model: "claude-sonnet-5",
			want:  []string{"chat", "--agent", "ctxloom", "--model", "claude-sonnet-5", "--no-interactive", "x"},
		},
		{
			name:      "effort and agent-engine from config",
			configure: &KiroConfig{Effort: "high", AgentEngine: "v3"},
			req:       &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}},
			want:      []string{"chat", "--agent", "ctxloom", "--effort", "high", "--agent-engine", "v3", "--no-interactive", "x"},
		},
		{
			name: "auto approve trusts all tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-all-tools", "--no-interactive", "x"},
		},
		{
			// SkipSetup (minimal/distill) always maps to the read-only allowlist,
			// overriding a requested bypass and also suppressing agent selection
			// — matching codex's/agy's identical SkipSetup-wins switch shape.
			name: "skip setup forces read-only trust-tools even over requested bypass",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--trust-tools=fs_read", "--no-interactive", "x"},
		},
		{
			name: "plan trusts read-only tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-tools=fs_read", "--no-interactive", "x"},
		},
		{
			name: "acceptEdits trusts read+write tools",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"chat", "--agent", "ctxloom", "--trust-tools=fs_read,fs_write", "--no-interactive", "x"},
		},
		{
			name:      "custom agent name",
			configure: &KiroConfig{Agent: "reviewer"},
			req:       &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "hi"}},
			want:      []string{"chat", "--agent", "reviewer", "hi"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewKiro()
			if tt.configure != nil {
				b.Configure(tt.configure)
			}
			assert.Equal(t, tt.want, b.buildArgs(tt.req, tt.model))
		})
	}
}

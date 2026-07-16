package kiro

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

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

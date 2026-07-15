package antigravity

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

func TestAntigravityConfig_BackendType(t *testing.T) {
	assert.Equal(t, "antigravity", AntigravityConfig{}.BackendType())
}

func TestNewAntigravity_Defaults(t *testing.T) {
	b := NewAntigravity()
	assert.Equal(t, "antigravity", b.Name())
	assert.Equal(t, "agy", b.BinaryPath)
}

func TestAntigravity_Configure(t *testing.T) {
	b := NewAntigravity()
	b.Configure(&AntigravityConfig{
		BinaryPath: "/opt/agy",
		Args:       []string{"--sandbox"},
		Env:        map[string]string{"FOO": "bar"},
	})
	assert.Equal(t, "/opt/agy", b.BinaryPath)
	assert.Equal(t, []string{"--sandbox"}, b.Args)
	assert.Equal(t, "bar", b.Env["FOO"])
}

func TestAntigravity_ConfigureIgnoresForeignConfig(t *testing.T) {
	b := NewAntigravity()
	b.Configure(nil)
	assert.Equal(t, "agy", b.BinaryPath)
}

func TestAntigravity_BuildArgs(t *testing.T) {
	tests := []struct {
		name  string
		req   *agent.ExecuteRequest
		model string
		want  []string
	}{
		{
			name:  "oneshot prompt",
			req:   &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "do it"}},
			model: "",
			want:  []string{"-p", "do it"},
		},
		{
			name:  "interactive prompt",
			req:   &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "do it"}},
			model: "",
			want:  []string{"-i", "do it"},
		},
		{
			name:  "model pinned",
			req:   &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}},
			model: "gemini-3-pro",
			want:  []string{"--model", "gemini-3-pro", "-p", "x"},
		},
		{
			name:  "auto approve",
			req:   &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			model: "",
			want:  []string{"--dangerously-skip-permissions", "-p", "x"},
		},
		{
			// SkipSetup (minimal/distill) always maps to read-only, overriding a
			// requested bypass — matching codex's SkipSetup->read-only precedence
			// (codex.buildArgs' identical switch shape). A distillation run must
			// never widen to bypass just because the label's configured posture
			// happens to be bypass.
			name: "skip setup forces plan mode even over requested bypass",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Permissions: agent.PermissionBypass, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"--mode", "plan", "-p", "x"},
		},
		{
			name: "plan mode",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"--mode", "plan", "-p", "x"},
		},
		{
			name: "accept-edits mode",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"--mode", "accept-edits", "-p", "x"},
		},
		{
			name: "default permission leaves agy's own prompting",
			req:  &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionDefault, Prompt: &agent.Fragment{Content: "x"}},
			want: []string{"-p", "x"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewAntigravity()
			assert.Equal(t, tt.want, b.buildArgs(tt.req, tt.model))
		})
	}
}

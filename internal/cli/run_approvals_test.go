package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestResolvePermissionMode pins the top-level run's permission-posture resolver:
// precedence (--permissions flag > agent binding > engine-label config > built-in
// default), the claude-code host-bypass default (the container-isolation stopgap,
// taskloom hilly-crop), and the headless-ONESHOT floor that upgrades a would-block
// posture (default/acceptEdits) to bypass so a run with no human can't hang.
func TestResolvePermissionMode(t *testing.T) {
	const claude = config.DefaultLLM // "claude-code"
	cases := []struct {
		name      string
		flag      string
		agentPerm string
		labelPerm string
		backend   string
		mode      pb.ExecutionMode
		want      agent.PermissionMode
	}{
		{"flag beats agent, label, and default", "plan", "bypass", "default", claude, pb.ExecutionMode_INTERACTIVE, agent.PermissionPlan},
		{"agent beats label and default", "", "acceptEdits", "bypass", claude, pb.ExecutionMode_INTERACTIVE, agent.PermissionAcceptEdits},
		{"label beats default", "", "", "plan", "codex", pb.ExecutionMode_INTERACTIVE, agent.PermissionPlan},
		{"claude-code default bypasses on the host", "", "", "", claude, pb.ExecutionMode_INTERACTIVE, agent.PermissionBypass},
		{"other backend default prompts", "", "", "", "codex", pb.ExecutionMode_INTERACTIVE, agent.PermissionDefault},
		{"invalid flag falls through to the default", "nonsense", "", "", claude, pb.ExecutionMode_INTERACTIVE, agent.PermissionBypass},
		{"oneshot upgrades a would-block default to bypass", "default", "", "", "codex", pb.ExecutionMode_ONESHOT, agent.PermissionBypass},
		{"oneshot keeps safe-headless plan", "plan", "", "", "codex", pb.ExecutionMode_ONESHOT, agent.PermissionPlan},
		{"oneshot upgrades acceptEdits to bypass", "acceptEdits", "", "", claude, pb.ExecutionMode_ONESHOT, agent.PermissionBypass},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvePermissionMode(tc.flag, tc.agentPerm, tc.labelPerm, tc.backend, tc.mode)
			assert.Equal(t, tc.want, got)
		})
	}
}

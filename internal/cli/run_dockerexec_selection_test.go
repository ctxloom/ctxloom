package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestSelectsDockerExecInteractive is the golden on Phase 2a-A's arm selection:
// ONLY a container-policy INTERACTIVE non-structured run takes the docker-exec
// Launcher (never constructs a go-plugin client). Every other combination stays
// on SpawnClient + goplugin — a leak either way is the regression this pins.
func TestSelectsDockerExecInteractive(t *testing.T) {
	cases := []struct {
		name       string
		policyName string
		mode       pb.ExecutionMode
		structured bool
		want       bool
	}{
		{"container interactive", "container", pb.ExecutionMode_INTERACTIVE, false, true},
		{"container-worktree interactive", "container-worktree", pb.ExecutionMode_INTERACTIVE, false, true},
		{"container oneshot takes the Part B owned-run arm, not docker-exec", "container", pb.ExecutionMode_ONESHOT, false, false},
		{"container structured takes the Part B owned-run arm, not docker-exec", "container", pb.ExecutionMode_INTERACTIVE, true, false},
		{"none interactive stays goplugin", "none", pb.ExecutionMode_INTERACTIVE, false, false},
		{"worktree interactive stays goplugin", "worktree", pb.ExecutionMode_INTERACTIVE, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, selectsDockerExecInteractive(tc.policyName, tc.mode, tc.structured))
		})
	}
}

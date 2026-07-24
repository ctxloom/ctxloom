package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// TestSelectsOwnedRunContainer is the golden on Phase 2a-B's arm selection: a
// CONTAINER policy that is --structured OR a --print oneshot takes the
// Transport 2 / EngineHost owner-owned run arm (never constructs a go-plugin
// client). Container interactive is Part A's docker-exec arm; host/worktree of
// ANY mode stays on SpawnClient + go-plugin (they never had the mauve-state
// problem). Together with TestSelectsDockerExecInteractive this pins that a
// container policy NEVER reaches SpawnClient, and a host/worktree policy ALWAYS
// does — the exact regression a leak either way would be.
func TestSelectsOwnedRunContainer(t *testing.T) {
	cases := []struct {
		name       string
		policyName string
		mode       pb.ExecutionMode
		structured bool
		want       bool
	}{
		{"container structured", "container", pb.ExecutionMode_INTERACTIVE, true, true},
		{"container structured oneshot", "container", pb.ExecutionMode_ONESHOT, true, true},
		{"container oneshot print", "container", pb.ExecutionMode_ONESHOT, false, true},
		{"container-worktree structured", "container-worktree", pb.ExecutionMode_INTERACTIVE, true, true},
		{"container-worktree oneshot", "container-worktree", pb.ExecutionMode_ONESHOT, false, true},
		{"container interactive is Part A, not this arm", "container", pb.ExecutionMode_INTERACTIVE, false, false},
		{"none structured stays goplugin (host §0.4)", "none", pb.ExecutionMode_INTERACTIVE, true, false},
		{"none oneshot stays goplugin (host §0.4)", "none", pb.ExecutionMode_ONESHOT, false, false},
		{"worktree structured stays goplugin (host §0.4)", "worktree", pb.ExecutionMode_INTERACTIVE, true, false},
		{"worktree oneshot stays goplugin (host §0.4)", "worktree", pb.ExecutionMode_ONESHOT, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, selectsOwnedRunContainer(tc.policyName, tc.mode, tc.structured))
		})
	}
}

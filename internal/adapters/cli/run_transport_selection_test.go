package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// transportCases is the FULL cross-product of the two inputs that decide a
// top-level built-in run's transport: every policy-name class × both execution
// modes. It is written out rather than derived so the expected arm for each
// combination is stated, not computed by the same rule under test.
var transportCases = []struct {
	name       string
	policyName string
	mode       pb.ExecutionMode
	want       runTransportArm
}{
	// Container, interactive → Phase 2a-A docker-exec.
	{"container interactive", "container", pb.ExecutionMode_INTERACTIVE, armDockerExecInteractive},
	{"container-worktree interactive", "container-worktree", pb.ExecutionMode_INTERACTIVE, armDockerExecInteractive},

	// Container, oneshot → Phase 2a-B owner-owned run.
	{"container oneshot print", "container", pb.ExecutionMode_ONESHOT, armOwnedRunContainer},
	{"container-worktree oneshot", "container-worktree", pb.ExecutionMode_ONESHOT, armOwnedRunContainer},

	// Every host/worktree combination stays on go-plugin.
	{"none interactive", "none", pb.ExecutionMode_INTERACTIVE, armGoPlugin},
	{"none oneshot", "none", pb.ExecutionMode_ONESHOT, armGoPlugin},
	{"worktree interactive", "worktree", pb.ExecutionMode_INTERACTIVE, armGoPlugin},
	{"worktree oneshot", "worktree", pb.ExecutionMode_ONESHOT, armGoPlugin},
}

// TestRunTransport is the golden on transport-arm selection. It replaces two
// separate boolean predicates with one total decision, so the assertions that
// matter are the two absolutes: a container policy NEVER reaches SpawnClient,
// and a host/worktree policy ALWAYS does. A leak either way is the regression.
func TestRunTransport(t *testing.T) {
	for _, tc := range transportCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, runTransport(tc.policyName, tc.mode))
		})
	}
}

// The decision must be TOTAL: every input combination names an arm. Neither
// half of a two-predicate split could state this, because the go-plugin case
// was whatever both predicates happened to leave over — so a combination no
// predicate covered silently spawned a plugin client for a container policy.
func TestRunTransport_EveryCombinationNamesAnArm(t *testing.T) {
	modes := []pb.ExecutionMode{pb.ExecutionMode_INTERACTIVE, pb.ExecutionMode_ONESHOT}
	policies := []string{"container", "container-worktree", "none", "worktree"}
	seen := 0
	for _, p := range policies {
		for _, m := range modes {
			arm := runTransport(p, m)
			assert.Contains(t, []runTransportArm{armGoPlugin, armDockerExecInteractive, armOwnedRunContainer}, arm,
				"policy=%s mode=%v", p, m)
			seen++
		}
	}
	assert.Equal(t, len(transportCases), seen, "the golden table must cover the whole cross-product")
}

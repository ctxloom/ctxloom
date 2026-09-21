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
	// Container, interactive → the docker-exec turn into a runner container.
	{"container interactive", "container", pb.ExecutionMode_INTERACTIVE, armDockerExecInteractive},
	{"container-worktree interactive", "container-worktree", pb.ExecutionMode_INTERACTIVE, armDockerExecInteractive},

	// Everything else is an owner-owned run of the in-process coordinator.
	{"container oneshot print", "container", pb.ExecutionMode_ONESHOT, armOwnedRun},
	{"container-worktree oneshot", "container-worktree", pb.ExecutionMode_ONESHOT, armOwnedRun},
	{"none interactive", "none", pb.ExecutionMode_INTERACTIVE, armOwnedRun},
	{"none oneshot", "none", pb.ExecutionMode_ONESHOT, armOwnedRun},
	{"worktree interactive", "worktree", pb.ExecutionMode_INTERACTIVE, armOwnedRun},
	{"worktree oneshot", "worktree", pb.ExecutionMode_ONESHOT, armOwnedRun},
}

// TestRunTransport is the golden on transport-arm selection: one total
// decision, so no combination can fall into an unnamed arm.
func TestRunTransport(t *testing.T) {
	for _, tc := range transportCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, runTransport(tc.policyName, tc.mode))
		})
	}
}

// The decision must be TOTAL: every input combination names an arm, so a
// combination no predicate covered cannot fall through to an unnamed one.
func TestRunTransport_EveryCombinationNamesAnArm(t *testing.T) {
	modes := []pb.ExecutionMode{pb.ExecutionMode_INTERACTIVE, pb.ExecutionMode_ONESHOT}
	policies := []string{"container", "container-worktree", "none", "worktree"}
	seen := 0
	for _, p := range policies {
		for _, m := range modes {
			arm := runTransport(p, m)
			assert.Contains(t, []runTransportArm{armOwnedRun, armDockerExecInteractive}, arm,
				"policy=%s mode=%v", p, m)
			seen++
		}
	}
	assert.Equal(t, len(transportCases), seen, "the golden table must cover the whole cross-product")
}

package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// transportCases is the FULL cross-product of the three inputs that decide a
// top-level built-in run's transport: every policy-name class × both execution
// modes × structured on/off. It is written out rather than derived so the
// expected arm for each combination is stated, not computed by the same rule
// under test.
var transportCases = []struct {
	name       string
	policyName string
	mode       pb.ExecutionMode
	structured bool
	want       runTransportArm
}{
	// Container, interactive, not structured → Phase 2a-A docker-exec.
	{"container interactive", "container", pb.ExecutionMode_INTERACTIVE, false, armDockerExecInteractive},
	{"container-worktree interactive", "container-worktree", pb.ExecutionMode_INTERACTIVE, false, armDockerExecInteractive},

	// Container, structured or oneshot → Phase 2a-B owner-owned run.
	{"container structured", "container", pb.ExecutionMode_INTERACTIVE, true, armOwnedRunContainer},
	{"container oneshot print", "container", pb.ExecutionMode_ONESHOT, false, armOwnedRunContainer},
	{"container structured oneshot", "container", pb.ExecutionMode_ONESHOT, true, armOwnedRunContainer},
	{"container-worktree structured", "container-worktree", pb.ExecutionMode_INTERACTIVE, true, armOwnedRunContainer},
	{"container-worktree oneshot", "container-worktree", pb.ExecutionMode_ONESHOT, false, armOwnedRunContainer},
	{"container-worktree structured oneshot", "container-worktree", pb.ExecutionMode_ONESHOT, true, armOwnedRunContainer},

	// Every host/worktree combination stays on go-plugin.
	{"none interactive", "none", pb.ExecutionMode_INTERACTIVE, false, armGoPlugin},
	{"none structured", "none", pb.ExecutionMode_INTERACTIVE, true, armGoPlugin},
	{"none oneshot", "none", pb.ExecutionMode_ONESHOT, false, armGoPlugin},
	{"none structured oneshot", "none", pb.ExecutionMode_ONESHOT, true, armGoPlugin},
	{"worktree interactive", "worktree", pb.ExecutionMode_INTERACTIVE, false, armGoPlugin},
	{"worktree structured", "worktree", pb.ExecutionMode_INTERACTIVE, true, armGoPlugin},
	{"worktree oneshot", "worktree", pb.ExecutionMode_ONESHOT, false, armGoPlugin},
	{"worktree structured oneshot", "worktree", pb.ExecutionMode_ONESHOT, true, armGoPlugin},
}

// TestRunTransport is the golden on transport-arm selection. It replaces two
// separate boolean predicates with one total decision, so the assertions that
// matter are the two absolutes: a container policy NEVER reaches SpawnClient,
// and a host/worktree policy ALWAYS does. A leak either way is the regression.
func TestRunTransport(t *testing.T) {
	for _, tc := range transportCases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, runTransport(tc.policyName, tc.mode, tc.structured))
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
			for _, s := range []bool{false, true} {
				arm := runTransport(p, m, s)
				assert.Contains(t, []runTransportArm{armGoPlugin, armDockerExecInteractive, armOwnedRunContainer}, arm,
					"policy=%s mode=%v structured=%v", p, m, s)
				seen++
			}
		}
	}
	assert.Equal(t, len(transportCases), seen, "the golden table must cover the whole cross-product")
}

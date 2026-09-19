package isolation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestChainFor_NeverPlacesAContainerAfterAContainer pins the invariant that
// makes prepareChain's second operand an EQUIVALENT mutant rather than missing
// coverage. Same purpose and shape as TestNonePrepareWorkspace_CannotFail: the
// mutant cannot be killed by any test, so pin the property that makes it
// unkillable instead of leaving that fact in a comment nothing checks.
//
// prepareChain's downgrade guard reads
//
//	IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next)
//
// and `!IsContainerPolicyName(next) -> true` survives mutation because `next`
// is never container-backed on any reachable path. That is a property of
// chainFor, not an accident of the scenarios: the only construction that would
// place a container after a container is substituting one ownership mode for
// the other, which chainFor refuses outright ("Substituting the other ownership
// mode is never an option, in strict mode or under --degraded").
//
// So this asserts the shape of every chain chainFor can build. If a future
// chain ever does put a container in the next slot, this goes RED and the
// operand becomes live, killable, and load-bearing — which is exactly when
// somebody needs to know.
func TestChainFor_NeverPlacesAContainerAfterAContainer(t *testing.T) {
	// A reachable runtime, so the container-bearing shapes actually materialize.
	// With Host{} the container tier never enters the chain and the two shapes
	// this test exists to inspect would silently not be built — the
	// absence-satisfies-absence trap.
	rt := fakeRuntime{name: "docker", binary: "docker", available: true}

	for _, tc := range []struct {
		name     string
		axes     Axes
		wantLen  int
		wantHead bool // the chain's first tier is container-backed
	}{
		{"container + shared", Axes{Workspace: WorkspaceShared, Runtime: RuntimeContainerRootless}, 2, true},
		{"container + worktree", Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeContainerRootless}, 3, true},
		{"host + worktree", Axes{Workspace: WorkspaceWorktree, Runtime: RuntimeHost}, 2, false},
		{"host + shared", Axes{Workspace: WorkspaceShared, Runtime: RuntimeHost}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetStrictness(t)
			stubRuntimeProbe(t, rt)

			chain := chainFor(tc.axes, "claude-code", ImageConfig{})
			require.Len(t, chain, tc.wantLen,
				"chainFor built a shape this invariant has never inspected; extend the table before trusting it")
			assert.Equal(t, tc.wantHead, IsContainerPolicyName(chain[0].Name()),
				"the first tier's container-ness is what makes the guard's FIRST operand meaningful")

			for i := 0; i+1 < len(chain); i++ {
				if !IsContainerPolicyName(chain[i].Name()) {
					continue
				}
				assert.False(t, IsContainerPolicyName(chain[i+1].Name()),
					"tier %d (%q) is container-backed and so is its successor %q: prepareChain's "+
						"!IsContainerPolicyName(next) is now LIVE and must be covered by a real test",
					i, chain[i].Name(), chain[i+1].Name())
			}

			// prepareChain substitutes None{}.Name() for the last tier's `next`,
			// so the terminal case must satisfy the same invariant.
			assert.False(t, IsContainerPolicyName(None{}.Name()),
				"prepareChain's terminal `next` must never be container-backed")
		})
	}
}

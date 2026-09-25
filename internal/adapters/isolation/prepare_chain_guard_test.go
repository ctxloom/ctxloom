package isolation

import (
	"context"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/require"
)

// isolation.prepareChain raises a ClassIsolation finding when a tier fails AND
// the boundary being lost is a CONTAINER one. The guard is
//
//	IsContainerPolicyName(p.Name()) && !IsContainerPolicyName(next)
//
// The acceptance row covers the firing case: a container fails, the finding is
// raised. It cannot cover the GUARD, because there p is a container and next is
// "none", so both operands are already true and replacing either with `true`
// changes nothing.
//
// This is the other side: a NON-container tier failing is an ordinary
// workspace-axis degrade and must raise NO finding. Without it, the first
// operand is free — a run that merely lost its worktree would abort as though
// it had lost its sandbox.
func TestPrepareChain_NonContainerDegradeRaisesNoFinding(t *testing.T) {
	mark := strictness.Checkpoint()

	pol, ws := prepareChain(context.Background(),
		[]Policy{failingPolicy{name: "worktree"}, passingPolicy{name: "none"}},
		RuntimeHost, t.TempDir(), "member-a")

	require.Equal(t, "none", pol.Name(), "the chain must degrade past the failed worktree")
	require.NotNil(t, ws, "a degraded run still gets a workspace")
	require.Empty(t, strictness.Since(mark),
		"losing a WORKTREE is a workspace-axis degrade, not a lost sandbox; raising an isolation finding here would abort runs that are not unsafe")
}

// The firing side, asserted here too so the pair reads together and neither can
// be weakened alone: losing a CONTAINER boundary IS a fatal isolation finding —
// for EVERY container-backed policy, not just the plain one. The
// container-worktree rows are the case that would otherwise go unguarded: if
// the predicate stopped recognising that name, its loss would degrade to the
// host with a warning and the unsandboxed run would launch.
func TestPrepareChain_LostContainerBoundaryRaisesIsolationFinding(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container string
		next      string
	}{
		{"container to host", PolicyNameContainer, "none"},
		{"container-worktree to host worktree", PolicyNameContainerWorktree, "worktree"},
		{"container-worktree to host", PolicyNameContainerWorktree, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stubRuntimeCandidates(t)
			mark := strictness.Checkpoint()

			pol, ws := prepareChain(context.Background(),
				[]Policy{failingPolicy{name: tc.container}, passingPolicy{name: tc.next}},
				RuntimeContainerRootless, t.TempDir(), "member-a")

			require.Equal(t, tc.next, pol.Name(), "the chain still degrades so the run gets a workspace")
			require.NotNil(t, ws)

			found := strictness.Since(mark)
			require.Len(t, found, 1, "a lost container boundary must raise exactly one finding")
			require.Equal(t, strictness.ClassIsolation, found[0].Class)
			require.Contains(t, found[0].Message, "member-a", "the finding must name the agent that lost its sandbox")
		})
	}
}

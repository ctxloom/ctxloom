package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A binding's may_delegate is journaled with its run, and the run's own
// agent_run is checked against it: a listed role launches, any other is
// refused naming the roles it may launch, and nothing is resolved for it.
func TestAgentRun_MayDelegateRestrictsTheCaller(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"lead":   {perm: "bypass", mayDelegate: []string{"finder"}},
		"finder": {perm: "plan"},
		"coder":  {perm: "acceptEdits"},
	}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)

	lead, err := c.AgentRun(context.Background(), ownerIdentity(), "lead", "coordinate", "", "")
	require.NoError(t, err, "the root session's delegation is not restricted")
	childHome(t, c, lead.RunID)
	caller := c.inProject(Identity{Harp: lead.Harp, RunID: lead.RunID, Depth: 1})

	resolved := len(sp.resolved)
	_, err = c.AgentRun(context.Background(), caller, "coder", "write it", "", "")
	require.ErrorIs(t, err, ErrDelegationRefused)
	assert.ErrorContains(t, err, `agent "lead" may launch only finder, not "coder"`)
	assert.Len(t, sp.resolved, resolved, "nothing is resolved for a refused role")

	finder, err := c.AgentRun(context.Background(), caller, "finder", "look it up", "", "")
	require.NoError(t, err, "a listed role launches")
	childHome(t, c, finder.RunID)
}

// Unset may_delegate permits any role.
func TestAgentRun_NoMayDelegatePermitsAny(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"lead": {perm: "bypass"}, "coder": {perm: "acceptEdits"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	lead, err := c.AgentRun(context.Background(), ownerIdentity(), "lead", "coordinate", "", "")
	require.NoError(t, err)
	childHome(t, c, lead.RunID)
	coder, err := c.AgentRun(context.Background(), c.inProject(Identity{Harp: lead.Harp, RunID: lead.RunID, Depth: 1}), "coder", "write it", "", "")
	require.NoError(t, err)
	childHome(t, c, coder.RunID)
}

// startRootRun registers the session owner and starts its owned run over a
// launch at posture label, from a binding whose may_delegate is mayDelegate.
func startRootRun(ctx context.Context, t *testing.T, c *Coordinator, harp, label string, mayDelegate []string) (Identity, *RunOutcome) {
	t.Helper()
	token, err := c.RegisterSessionOwner(harp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)
	l := ownerLaunch(harp, "claude-code", "fast", "sonnet", "/work", "bypass")
	l.Permission.Posture.Label = label
	spec := ownerRun(l, false)
	spec.MayDelegate = mayDelegate
	starter, _ := ownerRunStarter(ctx, &scriptedChat{}, "claude-code")
	out, err := c.StartOwnedRun(ctx, owner, spec, starter, "lead the work")
	require.NoError(t, err)
	return owner, out
}

// The ROOT session's binding is held to its may_delegate on the same path a
// child's is: its owned run journals the binding's list, and the root's own
// agent_run — under the owner credential or the run's own — is refused a
// role outside it, before anything is resolved.
func TestAgentRun_MayDelegateRestrictsTheRoot(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"finder": {perm: "plan"}, "coder": {perm: "acceptEdits"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, root := startRootRun(ctx, t, c, "owner-harp-lead", "bypass permissions", []string{"finder"})

	resolved := len(sp.resolved)
	for _, caller := range []Identity{owner, c.inProject(Identity{Harp: root.Harp, RunID: root.RunID})} {
		_, err := c.AgentRun(ctx, caller, "coder", "write it", "", "")
		require.ErrorIs(t, err, ErrDelegationRefused)
		assert.ErrorContains(t, err, `may launch only finder, not "coder"`)
	}
	assert.Len(t, sp.resolved, resolved, "nothing is resolved for a refused role")

	finder, err := c.AgentRun(ctx, owner, "finder", "look it up", "", "")
	require.NoError(t, err, "a listed role launches from the root")
	childHome(t, c, finder.RunID)
}

// A root binding with no may_delegate delegates freely.
func TestAgentRun_RootWithoutMayDelegatePermitsAny(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{"coder": {perm: "acceptEdits"}}, nil)
	c := newTestCoordinatorDepthCap(t, sp, nil, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, _ := startRootRun(ctx, t, c, "owner-harp-free", "bypass permissions", nil)
	coder, err := c.AgentRun(ctx, owner, "coder", "write it", "", "")
	require.NoError(t, err)
	childHome(t, c, coder.RunID)
}

// The roster shows an owner run's posture by its engine's display name,
// which its launch carries.
func TestStartOwnedRun_RosterShowsThePosture(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, root := startRootRun(ctx, t, c, "owner-harp-posture", "bypass permissions", nil)
	var got *RunInfo
	for _, r := range c.ListRuns(true, "").Runs {
		if r.RunID == root.RunID {
			got = &r
		}
	}
	require.NotNil(t, got)
	assert.Equal(t, "bypass permissions", got.PermissionMode)
}

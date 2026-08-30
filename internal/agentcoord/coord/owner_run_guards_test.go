package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// The runner-launch failure path wrapped the error for failChild
// ("owner run: runner launch failed: %w") and then returned the RAW error to
// the caller, so the operator-facing message — the one that reaches `ctxloom
// run`'s stderr — was the bare spawner error with no indication of which stage
// of which run produced it, while the richer text went only into the run's own
// terminal record.
func TestStartOwnedRun_LaunchFailureReturnsTheWrappedError(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-launch-fail"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	boom := errors.New("no such image: ctxloom-agent-claude")
	starter := func(context.Context, map[string]string) (func(), string, error) { return nil, "", boom }

	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast",
		WorkDir: "/work", Permission: agent.PermissionBypass,
	}, starter, "hello")

	if !assert.Error(t, err) {
		return
	}
	assert.ErrorIs(t, err, boom, "the cause must stay unwrappable to the caller")
	assert.Contains(t, err.Error(), "owner run: runner launch failed",
		"the caller must get the same wrapped context failChild got, not the bare spawner error")
}

// REFUTED, pinned: the register claimed StartOwnedRun's
// issueStartRun error path is "the ONE path where the coordinator's own
// internal cleanup never fires", because it returns bare while the two paths
// above it call c.failChild. It is not: issueStartRun calls c.failChild itself
// on every one of its error returns (awaitRunner timeout, payload guard,
// StartRun wire failure, StartRun refusal), which
// TestStartOwnedRun_CleansUpOnIssueStartRunFailure already pins from the
// observable side (kill fired, run out of the live roster).
//
// This pins the half that makes the row's proposed remediation WRONG: cleanup
// happens EXACTLY ONCE. failChild feeds noteLaunchFailure, which spends the
// launch-retry budget, so adding a second c.failChild at the call site would
// double-count every failed owner-run launch and halve the budget that exists
// to stop a runaway relaunch loop. Apply the row's fix and this goes red.
func TestStartOwnedRun_IssueStartRunFailureCountsOneLaunchFailure(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c, err := New(Options{
		ProjectDir:         t.TempDir(),
		StateDir:           t.TempDir(),
		Spawner:            sp,
		RunnerAwaitTimeout: 100 * time.Millisecond, // issueStartRun's awaitRunner budget
	})
	require.NoError(t, err)
	require.NoError(t, c.Serve())
	t.Cleanup(c.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-failchild-once"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	// Launches "successfully" but never dials home: issueStartRun's awaitRunner
	// times out and it fails the child itself.
	starter := func(context.Context, map[string]string) (func(), string, error) { return func() {}, "", nil }

	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast",
		WorkDir: "/work", Permission: agent.PermissionBypass,
	}, starter, "hello")
	if !assert.Error(t, err, "a runner that never dials home must fail the owner run") {
		return
	}

	c.mu.Lock()
	fails := c.launchGateLocked(ownerHarp).fails
	c.mu.Unlock()
	assert.Equal(t, 1, fails,
		"ONE failed launch must spend ONE unit of the launch-retry budget: a second failChild at the "+
			"StartOwnedRun call site would double-count it and halve the budget that bounds a runaway loop")
}

// SendOwnedRunTurn resolved ANY live run id from c.attach, so a
// delegated child's run id was accepted. That queues self-addressed mail
// (from == to == the child's harp) which the child's own turn-boundary drain
// then delivers to it as a user turn, with the coordinator's parent→child
// routing, audit trail and result bridge all bypassed. An owner-run verb must
// answer only for an owner run.
func TestSendOwnedRunTurn_RefusesADelegatedChildsRun(t *testing.T) {
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "hello", "", "")
	if !assert.NoError(t, err) {
		return
	}

	err = c.SendOwnedRunTurn(out.RunID, "sneak a turn in")
	if !assert.Error(t, err, "a delegated child's run id must not be drivable through the owner-run verb") {
		return
	}
	assert.Contains(t, err.Error(), "not an owner-owned run")
	assert.Equal(t, 0, c.pendingCount(out.Harp),
		"nothing may be queued for the child — the refusal must happen before the enqueue")
}

// REFUTED, pinned: the register claimed the three post-enqueueRun
// mutations (viaStartRun/ownerRun/oneshot) leave a window in which "a dialing
// runner or a racing terminal observes a half-built rt". They are applied under
// c.mu (so never a data race) and, decisively, BEFORE the runner exists at all:
// StartOwnedRun mints them and only then calls start(), which is the first
// moment any runner can dial home.
//
// The invariant that actually matters is that ordering, so that is what is
// pinned: by the time the starter runs, every flag is already set. Move the
// mutations after start() — the shape the row describes — and this goes red.
func TestStartOwnedRun_FlagsAreSetBeforeTheRunnerCanExist(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-flag-order"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	type snapshot struct{ viaStartRun, ownerRun, oneshot, seen bool }
	var got snapshot
	starter := func(context.Context, map[string]string) (func(), string, error) {
		c.mu.Lock()
		if rt := c.byHarp[ownerHarp]; rt != nil {
			got = snapshot{viaStartRun: rt.viaStartRun, ownerRun: rt.ownerRun, oneshot: rt.oneshot, seen: true}
		}
		c.mu.Unlock()
		return nil, "", errors.New("stop here: the flags have already been observed")
	}

	_, err = c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast",
		WorkDir: "/work", Permission: agent.PermissionBypass, Oneshot: true,
	}, starter, "hello")
	if !assert.Error(t, err) {
		return
	}

	if !assert.True(t, got.seen, "the run must be registered by the time the runner is launched") {
		return
	}
	assert.True(t, got.viaStartRun, "viaStartRun must be set before any runner can dial home")
	assert.True(t, got.ownerRun, "ownerRun must be set before any runner can dial home")
	assert.True(t, got.oneshot, "oneshot must be set before any runner can dial home")
}

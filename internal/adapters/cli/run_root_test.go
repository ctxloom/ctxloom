package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// withNopSpawner composes every coordinator this test builds over a spawner
// that launches nothing.
func withNopSpawner(t *testing.T) {
	t.Helper()
	prev := theComposition.NewCoordinator
	theComposition.NewCoordinator = func(app *operations.App, opts coord.Options) (*coord.Coordinator, error) {
		opts.Spawner = coordharness.NopSpawner{}
		return prev(app, opts)
	}
	t.Cleanup(func() { theComposition.NewCoordinator = prev })
}

// hostRootFor stands the session's coordinator up the way `ctxloom run` does
// and returns the harp its root is named by.
func hostRootFor(t *testing.T, activeHarp, resume string) string {
	t.Helper()
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	withNopSpawner(t)
	prev := runResumeSession
	runResumeSession = resume
	t.Cleanup(func() { runResumeSession = prev })

	st := &runState{workDir: t.TempDir(), activeHarp: activeHarp}
	teardown := st.hostCoordinator()
	t.Cleanup(teardown)
	require.NotNil(t, st.sessionCoord, "precondition: the session hosts a coordinator")
	return filepath.Base(st.sessionCoord.StateDir())
}

// A fresh `ctxloom run` founds a root of its own, named by the harp it just
// minted: it never claims, adopts or is refused by another session's tree.
func TestHostCoordinator_AFreshRunFoundsItsOwnRoot(t *testing.T) {
	assert.Equal(t, "fresh-harp", hostRootFor(t, "fresh-harp", ""))
}

// `ctxloom run --session H` mints a NEW harp for itself, yet claims root H:
// the resumed session's tree, whose runs it adopts.
func TestHostCoordinator_ResumeClaimsTheResumedRoot(t *testing.T) {
	assert.Equal(t, "resumed-harp", hostRootFor(t, "new-harp", "resumed-harp"))
}

// A session's root outlives its clean exit: `ctxloom run --session H` adopts
// it later with H's ended children. Only the sweep removes it.
func TestHostCoordinator_ACleanExitKeepsTheSessionRoot(t *testing.T) {
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	withNopSpawner(t)
	st := &runState{workDir: t.TempDir(), activeHarp: "clean-exit-harp"}
	teardown := st.hostCoordinator()
	require.NotNil(t, st.sessionCoord)
	dir := st.sessionCoord.StateDir()

	teardown()

	assert.DirExists(t, dir)
}

// hostQueueFor hosts the session's coordinator the way `ctxloom run` does,
// for a launch in mode, and returns its approval queue — the one every
// depth's requests park in.
func hostQueueFor(t *testing.T, mode engine.Mode) *coord.ApprovalQueue {
	t.Helper()
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	withNopSpawner(t)
	st := &runState{workDir: t.TempDir(), activeHarp: "approvals-harp", launch: launch.Launch{Mode: mode}}
	teardown := st.hostCoordinator()
	t.Cleanup(teardown)
	require.NotNil(t, st.sessionCoord, "precondition: the session hosts a coordinator")
	return st.sessionCoord.Approvals()
}

var childAsker = coord.Identity{Harp: "child-harp", RunID: "run-1", Depth: 1}

func bashAsk() coord.PendingApproval {
	return coord.PendingApproval{Agent: "worker", Ask: engine.PermissionAsk{Tool: "Bash", Input: json.RawMessage(`{"command":"make"}`)}}
}

// A one-shot root presents no approvals, so hosting its coordinator marks
// the queue unattended: a child's request is refused the moment it parks,
// naming why — decided at the root, learned by the child from the queue its
// request lands in.
func TestHostCoordinator_AOneShotRootRefusesAtOnce(t *testing.T) {
	q := hostQueueFor(t, engine.Structured)
	// Bounded, so a request wrongly parked ends as withdrawn, not a hang.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	start := time.Now()
	d := q.Park(ctx, childAsker, bashAsk(), 20*time.Minute)
	assert.Less(t, time.Since(start), time.Second, "refused at once, not held toward its 20m timeout")
	assert.False(t, d.Allow)
	assert.Equal(t, agent.DeciderRefused, d.Decider)
	assert.Equal(t, unattendedOneShot, d.Message)
}

// An interactive root is not marked when its coordinator is hosted: its
// terminal layer, built later (prepareSessionIO), presents the queue, and a
// child's request is parked for the modal, which answers it.
func TestHostCoordinator_AnInteractiveRootParksForTheModal(t *testing.T) {
	q := hostQueueFor(t, engine.Interactive)
	events := q.Subscribe(t.Context())
	got := make(chan coord.ApprovalDecision, 1)
	go func() { got <- q.Park(t.Context(), childAsker, bashAsk(), 20*time.Minute) }()
	var added coord.QueueEvent
	select {
	case added = <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("the request was never parked for a presenter")
	}
	require.Equal(t, coord.QueueAdded, added.Kind)
	require.Len(t, q.Pending(), 1)
	require.NoError(t, q.Answer(added.ID, coord.ApprovalDecision{Allow: true}))
	d := <-got
	assert.True(t, d.Allow)
	assert.Equal(t, agent.DeciderHuman, d.Decider)
}

// An internal one-shot's coordinator (distill, init's probe) is an ephemeral
// root nothing resumes: closing it at the command's end removes its root.
func TestInternalCoordinator_ClosingRemovesItsEphemeralRoot(t *testing.T) {
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	withNopSpawner(t)
	t.Cleanup(closeInternalCoordinator)
	c, err := internalCoordinator(t.TempDir(), "one-shot-harp")
	require.NoError(t, err)
	dir := c.StateDir()
	require.DirExists(t, dir)

	closeInternalCoordinator()

	assert.NoDirExists(t, dir)
}

// Nothing presents an internal one-shot's approval queue: a request there is
// refused at once rather than held for its timeout.
func TestInternalCoordinator_RefusesApprovalsAtOnce(t *testing.T) {
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	testApp(t)
	withNopSpawner(t)
	t.Cleanup(closeInternalCoordinator)
	c, err := internalCoordinator(t.TempDir(), "one-shot-harp")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	d := c.Approvals().Park(ctx, childAsker, bashAsk(), 20*time.Minute)
	assert.Equal(t, agent.DeciderRefused, d.Decider)
	assert.Equal(t, unattendedOneShot, d.Message)
}

package coord

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// NOTE on assertion style: see mailbox_takefail_test.go's cute-brink note —
// Coordinator.Close runs in t.Cleanup and a require.* FailNow inside a coord
// test deadlocks it. assert + return in the tests that hold a coordinator.

// U023-F06: neither the mailbox nor SendOwnedRunTurn rejected an EMPTY body. An
// empty agent_send was journaled as a durable fact, "delivered" to a parked
// recv, and answered with the ordinary success disposition — a message with
// zero payload reported as a delivery. The receiving agent is woken for a turn
// whose content is nothing at all, which is this project's characteristic
// silent no-op: every signal green, no bytes delivered.
//
// These tests assert the PAYLOAD (what is queued / what the recipient can
// receive), never an exit code.
func TestQueueMail_RefusesAnEmptyBody(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-a"
	for _, body := range []string{"", "   ", "\n\t "} {
		_, _, err := c.queueMailPayloadID("m-empty", "parent", role, "task", body, nil, "")
		if !assert.Error(t, err, "an empty body (%q) must be refused, not queued", body) {
			return
		}
		if !assert.Equal(t, 0, c.pendingCount(role),
			"nothing may be journaled for an empty body (%q) — the payload assertion, not the error", body) {
			return
		}
	}

	// A recv finds nothing: no phantom delivery was made.
	msgs, err := c.recvMail(context.Background(), role, 0)
	assert.Empty(t, msgs)
	assert.ErrorIs(t, err, ErrRecvTimeout)
}

// TestQueueMail_StructuredOnlyMessageIsStillAllowed keeps the guard from
// breaking the one legitimate body-less shape: a message whose payload IS its
// structured companion (the escalation ladder's relayed ApprovalRequest
// projection carries both, and a reply may carry only the structure).
func TestQueueMail_StructuredOnlyMessageIsStillAllowed(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)

	const role = "child-b"
	_, _, err := c.queueMailPayloadID("m-structured", "parent", role, "approval_reply", "",
		json.RawMessage(`{"decision":"accept"}`), "")
	if !assert.NoError(t, err, "a structured-only message carries a payload and must be queued") {
		return
	}
	assert.Equal(t, 1, c.pendingCount(role))
}

// TestAgentSend_RefusesAnEmptyBody is the operator-visible half: the send verb
// behind the agent_send MCP tool must not answer "sent" for nothing.
func TestAgentSend_RefusesAnEmptyBody(t *testing.T) {
	sp := newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", profiles: []string{"p1"}}}, nil)
	c := newTestCoordinator(t, sp, nil)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "hello", "", "")
	if !assert.NoError(t, err) {
		return
	}

	disposition, err := c.AgentSend(ownerIdentity(), out.Harp, "message", "  ", nil, "")
	assert.Error(t, err, "an empty agent_send must fail loudly, not report a disposition")
	assert.Empty(t, disposition)
}

// TestSendOwnedRunTurn_RefusesAnEmptyTurn is the owner-run half: an empty
// follow-up turn wakes the engine with nothing to do, and its own prompt source
// (the host's stdin/composed context) is the thing that actually failed.
func TestSendOwnedRunTurn_RefusesAnEmptyTurn(t *testing.T) {
	sp := newFakeSpawner(nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-empty-turn"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	sc := &scriptedChat{}
	starter, _ := ownerRunStarter(ctx, sc, "claude-code")
	outcome, err := c.StartOwnedRun(ctx, owner, OwnerRunSpec{
		Harp: ownerHarp, Backend: "claude-code", Label: "fast", Model: "sonnet",
		WorkDir: "/work", Permission: agent.PermissionBypass,
	}, starter, "turn one")
	if !assert.NoError(t, err) {
		return
	}

	err = c.SendOwnedRunTurn(outcome.RunID, "")
	if !assert.Error(t, err, "an empty owner-run turn must be refused") {
		return
	}
	assert.Equal(t, 0, c.pendingCount(ownerHarp), "nothing may be queued for an empty turn")
}

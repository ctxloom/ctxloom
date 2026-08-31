package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/agents"
)

// TestHandleAgentRecv_NamesAnUndecodableStructuredCompanion pins the fix
// below.
//
// The mailbox carries `structured` as raw bytes, and the stdio agent_recv
// projected it with `if json.Unmarshal(...) == nil` — so anything that would
// not decode into an object was dropped on the floor while the body was
// delivered and the call reported success. That is this project's
// consume-then-decode shape: Coordinator.recvMail marks the batch DELIVERED
// before returning, so the discarded field is gone for good, not redelivered.
//
// It matters most exactly where it is least visible: when the companion is
// the part that carries the meaning, a recipient stripped of it sees what
// looks like a courtesy note and acts on the wrong thing — so the fault has
// to be NAMED rather than presented as an absent field.
func TestHandleAgentRecv_NamesAnUndecodableStructuredCompanion(t *testing.T) {
	cfg, c, _ := buildHostCoordinator(t, map[string]agents.Agent{
		"worker": headlessAgent("p1"),
	})
	self := coord.Identity{Harp: "coordinator-harp", Depth: 0}
	s := &ctxServer{
		cfg:    cfg,
		self:   self,
		agents: &agentDelegation{self: self, c: c},
	}

	_, runOut, err := s.handleAgentRun(context.Background(), nil, agentRunInput{Agent: "worker", Prompt: "go"})
	require.NoError(t, err)
	require.NotEmpty(t, runOut.Harp)

	// A structured companion that is valid JSON but not an object — the shape
	// a map[string]any decode rejects. Sent by the child, addressed at its
	// parent, exactly as a real question would be.
	child := coord.Identity{Harp: runOut.Harp, Depth: 1}
	_, err = c.AgentSend(child, "parent", "question", "may I proceed?", []byte(`["decision","ACCEPT"]`), "")
	require.NoError(t, err)

	var found *agentBusMessage
	require.Eventually(t, func() bool {
		_, out, rerr := s.handleAgentRecv(context.Background(), nil, agentRecvInput{Wait: 1})
		if rerr != nil || out == nil {
			return false
		}
		for i := range out.Messages {
			if out.Messages[i].Body == "may I proceed?" {
				found = &out.Messages[i]
				return true
			}
		}
		return false
	}, 10*time.Second, 20*time.Millisecond, "the child's question never reached the coordinator's mailbox")

	assert.Empty(t, found.Structured, "an undecodable companion must not be presented as an empty object")
	assert.NotEmpty(t, found.StructuredError,
		"the message must SAY its structured companion could not be decoded — it was already acked as delivered, so silence loses it permanently")
}

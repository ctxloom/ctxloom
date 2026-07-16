package acpagent

// L1b — SELF-DRIVE (ACP H1 conformance harness, second layer). ctxloom
// speaks ACP in BOTH roles: this package is the AGENT half (ctxloom acp,
// served here via Serve); internal/acp is the CLIENT half (driving another
// agent's `<agent> acp` subprocess — kiro/codex/claude's own ACP-mode
// backends embed it, and the generic "acp" LLM entry is it directly). This
// file points ctxloom's own CLIENT at ctxloom's own AGENT: if the agent half
// ever emits a frame the client half can't decode (or vice versa), a real
// editor integration is broken and THIS test catches it in one shot, without
// needing a real editor or a real engine.
//
// HONEST SCOPE — read before trusting the headline "ctxloom drives ctxloom":
//
//   - What's real, unmodified production code on BOTH sides of the wire:
//     internal/acp's ACP.Chat (client: spawn, JSON-RPC framing, mapping.go's
//     ACP<->agent.ChatEvent translation, permission-forward plumbing) driving
//     THIS package's Serve/Server (agent: session lifecycle, wire.go/mapping.go's
//     inverse translation) over a REAL OS process boundary (a real exec.Command,
//     real stdio pipes) — exactly the shape internal/claude/chat.go,
//     internal/codex/chat.go, internal/kiro/chat.go, and internal/opencode/chat.go
//     already trust in production.
//   - What's SUBSTITUTED, and why (the identical rationale L1 states in
//     loopback_test.go's doc comment): the spawned agent process is
//     cmd/acpl1harness, not literally `ctxloom acp`. No registered backend is
//     both StructuredChat-capable AND hermetic/credential-free — `ctxloom acp`
//     would need a real, live-authenticated engine (claude/codex/kiro) behind
//     it, which this harness must never depend on. acpl1harness calls the
//     EXACT SAME acpagent.Serve `ctxloom acp` calls, with a deterministic
//     scripted engine (cmd/acpl1harness/engine.go) instead of a real LLM — so
//     every byte of THIS package's protocol-serving code is still 100% real.
//   - What is therefore NOT exercised here: `ctxloom acp`'s own command-line
//     wiring (acp_cmd.go), config/profile/agent resolution, context assembly,
//     the MCP trust gate, harp/session accounting, and any real engine's
//     actual behavior. Those are out of scope for this slice (owned by
//     other in-flight work) and are not this test's claim.
//
// Net claim: the ACP WIRE PROTOCOL round-trips correctly between ctxloom's
// own client mapping and ctxloom's own agent mapping — turn content, stop
// reasons (including the cancel-races-completion override), and forwarded
// permission requests all survive the round trip byte for byte. That is a
// real, narrow, honestly-scoped self-drive — not a claim that `ctxloom acp`
// against a real engine has been proven end to end.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// selfDriveTimeout bounds every event wait below; a hang here means one half
// of the round trip stopped speaking to the other mid-conversation.
const selfDriveTimeout = 10 * time.Second

// newSelfDriver builds a REAL internal/acp CLIENT (the exact production
// driver kiro/codex/claude/generic-acp backends embed) configured to spawn
// the L1 harness binary (built once per package test run by l1Binary, shared
// with loopback_test.go) instead of `ctxloom acp` — see the file doc comment
// for why.
func newSelfDriver(t *testing.T) *acp.ACP {
	t.Helper()
	bin := l1Binary(t)
	return acp.NewChatDriver(acp.ACPConfig{Command: bin})
}

// selfDriveConversation starts one self-driven chat and returns the live
// channels plus the goroutine's terminal error (buffered, readable once the
// conversation ends).
func selfDriveConversation(t *testing.T, ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error) {
	t.Helper()
	drv := newSelfDriver(t)
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent, 8)
	chatErr := make(chan error, 1)
	go func() { chatErr <- drv.Chat(ctx, req, in, out) }()
	return in, out, chatErr
}

func waitSelfDriveEvent(t *testing.T, out <-chan agent.ChatEvent) agent.ChatEvent {
	t.Helper()
	select {
	case ev, ok := <-out:
		require.True(t, ok, "self-drive: chat event stream closed unexpectedly")
		return ev
	case <-time.After(selfDriveTimeout):
		t.Fatal("self-drive: timed out waiting for a ChatEvent")
		return agent.ChatEvent{}
	}
}

// TestL1b_SelfDrive_BasicTurn: ctxloom's own ACP client drives ctxloom's own
// ACP agent through one ordinary turn over a real process boundary — the
// baseline proof the two halves speak the same wire language at all.
func TestL1b_SelfDrive_BasicTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out, chatErr := selfDriveConversation(t, ctx, agent.ChatRequest{WorkDir: t.TempDir()})

	sessionEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, sessionEv.Session, "the first self-driven event must be the session marker")

	in <- agent.ChatMessage{Text: "hello over self-drive"}
	entryEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, entryEv.Entry)
	assert.Equal(t, "echo: hello over self-drive", entryEv.Entry.Content,
		"ctxloom's own client must decode ctxloom's own agent's echoed content byte for byte")

	completeEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, completeEv.Complete)
	assert.Equal(t, "end_turn", completeEv.Complete.StopReason)

	close(in)
	require.NoError(t, <-chatErr)
}

// TestL1b_SelfDrive_CancelMidTurn: our client's ChatMessage.CancelTurn must
// reach our agent's cancel handling (session/cancel) and resolve the turn as
// "cancelled" — driven this time through OUR OWN client's ACP mapping, not
// the raw JSON-RPC assertions loopback_test.go already makes on the agent
// side alone.
func TestL1b_SelfDrive_CancelMidTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out, chatErr := selfDriveConversation(t, ctx, agent.ChatRequest{WorkDir: t.TempDir()})

	waitSelfDriveEvent(t, out) // session

	in <- agent.ChatMessage{Text: l1SentinelCancelMe}
	waiting := waitSelfDriveEvent(t, out)
	require.NotNil(t, waiting.Entry)
	assert.Equal(t, "waiting for cancel", waiting.Entry.Content)

	in <- agent.ChatMessage{CancelTurn: true}
	complete := waitSelfDriveEvent(t, out)
	require.NotNil(t, complete.Complete)
	assert.Equal(t, "cancelled", complete.Complete.StopReason,
		"our own client's CancelTurn must reach our own agent's cancel handling and resolve as cancelled")

	close(in)
	require.NoError(t, <-chatErr)
}

// TestL1b_SelfDrive_PermissionForwarding: our agent's forwarded permission
// request must surface through our OWN client as a ChatEvent.Permission, and
// our client's answer must round-trip back to the agent verbatim — the
// bidirectional half of the round trip loopback_test.go's raw JSON-RPC
// driver cannot exercise (it IS the client there; here our real client is
// under test too).
func TestL1b_SelfDrive_PermissionForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out, chatErr := selfDriveConversation(t, ctx, agent.ChatRequest{
		WorkDir:            t.TempDir(),
		ForwardPermissions: true,
	})

	waitSelfDriveEvent(t, out) // session

	in <- agent.ChatMessage{Text: l1SentinelPermission}

	toolEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, toolEv.Entry)
	assert.Equal(t, agent.EntryTypeToolUse, toolEv.Entry.Type)

	permEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, permEv.Permission, "our client must surface the agent's forwarded permission request as a ChatEvent")
	require.Len(t, permEv.Permission.Options, 2)
	assert.Equal(t, "allow", permEv.Permission.Options[0].ID)

	in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: permEv.Permission.ID, OptionID: "allow"}}

	answerEv := waitSelfDriveEvent(t, out)
	require.NotNil(t, answerEv.Entry)
	assert.Equal(t, "permission: allow", answerEv.Entry.Content,
		"the option id our client sent must round-trip through our agent to the engine verbatim")

	complete := waitSelfDriveEvent(t, out)
	require.NotNil(t, complete.Complete)
	assert.Equal(t, "end_turn", complete.Complete.StopReason)

	close(in)
	require.NoError(t, <-chatErr)
}

// TestL1b_SelfDrive_EngineErrorPropagates: a fatal agent-side error must
// surface through our OWN client as a real Go error carrying the VERBATIM
// message — never swallowed, never generic-wrapped — proving our client's
// error path (not just the agent's) actually works.
func TestL1b_SelfDrive_EngineErrorPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, out, chatErr := selfDriveConversation(t, ctx, agent.ChatRequest{WorkDir: t.TempDir()})

	waitSelfDriveEvent(t, out) // session

	in <- agent.ChatMessage{Text: l1SentinelFail}

	// Chat returns (closing `out`) on the fatal error; drain any trailing
	// events before reading the terminal error.
	for range out {
	}
	err := <-chatErr
	require.Error(t, err)
	assert.Contains(t, err.Error(), l1FailMessage, "our client must surface our agent's verbatim error text")
}

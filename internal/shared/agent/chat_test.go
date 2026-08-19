package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeChat is an exemplar StructuredChat that pins the interface contract:
//   - emits one Session event at the start;
//   - for each inbound ChatMessage, emits an assistant Entry + a Complete;
//   - returns when `in` is closed and drained, or ctx is cancelled;
//   - closes `out` exactly once before returning (producer owns out's close).
//
// The caller owns `in` and closes it to signal "no more input".
type fakeChat struct{}

func (fakeChat) Chat(ctx context.Context, req ChatRequest, in <-chan ChatMessage, out chan<- ChatEvent) error {
	defer close(out)
	send := func(ev ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(ChatEvent{Session: &ChatSessionInfo{Model: req.Model, ContextWindow: 1_000_000}}) {
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil // input closed & drained → conversation over
			}
			if !send(ChatEvent{Entry: &SessionEntry{Type: EntryTypeAssistant, Content: "echo: " + msg.Text}}) {
				return ctx.Err()
			}
			if !send(ChatEvent{Complete: &TurnMeta{InputTokens: len(msg.Text), ContextWindow: 1_000_000}}) {
				return ctx.Err()
			}
		}
	}
}

var _ StructuredChat = fakeChat{}

// TestStructuredChat_RoundTrip pins the contract: caller streams messages and
// closes `in`; the impl streams Session then per-message Entry+Complete and
// closes `out` on return.
func TestStructuredChat_RoundTrip(t *testing.T) {
	in := make(chan ChatMessage)
	out := make(chan ChatEvent)
	done := make(chan error, 1)
	go func() { done <- fakeChat{}.Chat(context.Background(), ChatRequest{Model: "m"}, in, out) }()

	var events []ChatEvent
	collected := make(chan struct{})
	go func() {
		for ev := range out {
			events = append(events, ev)
		}
		close(collected)
	}()

	in <- ChatMessage{Text: "hello"}
	in <- ChatMessage{Text: "world"}
	close(in)

	require.NoError(t, <-done)
	<-collected // Chat closed `out` on return

	require.Len(t, events, 5)
	require.NotNil(t, events[0].Session)
	assert.Equal(t, "m", events[0].Session.Model)
	assert.Equal(t, 1_000_000, events[0].Session.ContextWindow)
	require.NotNil(t, events[1].Entry)
	assert.Equal(t, EntryTypeAssistant, events[1].Entry.Type)
	assert.Equal(t, "echo: hello", events[1].Entry.Content)
	require.NotNil(t, events[2].Complete)
	assert.Equal(t, 5, events[2].Complete.InputTokens)
	require.NotNil(t, events[3].Entry)
	assert.Equal(t, "echo: world", events[3].Entry.Content)
	require.NotNil(t, events[4].Complete)
}

// TestStructuredChat_ContextCancelReturns: cancelling ctx ends the conversation
// promptly even with input still open.
func TestStructuredChat_ContextCancelReturns(t *testing.T) {
	in := make(chan ChatMessage)
	out := make(chan ChatEvent)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- fakeChat{}.Chat(ctx, ChatRequest{}, in, out) }()
	go func() { //nolint:revive // drain so the impl never blocks on a send
		for range out {
		}
	}()

	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("Chat did not return after context cancel")
	}
}

// TestACPTransport_RequireOnHost_AdapterMissing pins the gate every
// ACPAdapter engine's Chat() shares: a host-runtime chat (runtime != "")
// with the declared adapter binary NOT on PATH fails loud, naming both the
// binary and the exact InstallCmd from the declaration — never a second,
// hardcoded copy of the install string.
func TestACPTransport_RequireOnHost_AdapterMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // guaranteed empty: the binary cannot resolve
	transport := ACPTransport{
		Kind:       ACPAdapter,
		Binary:     "definitely-not-a-real-binary-xyz",
		InstallCmd: "npm install -g @zed-industries/definitely-not-a-real-binary-xyz",
	}
	err := transport.RequireOnHost("", "testengine")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "testengine", "must name the engine")
	assert.Contains(t, err.Error(), transport.Binary, "must name the missing binary")
	assert.Contains(t, err.Error(), transport.InstallCmd, "must give the exact install command from the declaration")
}

// TestACPTransport_RequireOnHost_ContainerRuntimeExempt pins the container
// exemption: a container runtime value means the AGENT IMAGE carries its own
// adapter, so this host process's PATH is irrelevant — RequireOnHost must
// return nil even though the binary genuinely resolves nowhere on this PATH.
//
// BOTH ownership modes are asserted. The exemption is about the image, which
// is identical either way, so a gate written as an equality test against one
// const would fail the other mode with a PATH error about a binary that was
// never going to be used — the reason the gate asks IsContainerRuntime.
func TestACPTransport_RequireOnHost_ContainerRuntimeExempt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	transport := ACPTransport{Kind: ACPAdapter, Binary: "definitely-not-a-real-binary-xyz", InstallCmd: "npm install -g whatever"}
	for _, runtime := range []RuntimeAxis{RuntimeContainerRootless, RuntimeContainerRootful} {
		assert.NoError(t, transport.RequireOnHost(runtime, "testengine"), "runtime %q", runtime)
	}
}

// TestIsContainerRuntime pins the predicate every containerization gate in
// this package funnels through: exactly the two container values, and nothing
// else. "container" in particular is NOT one of them — the pre-split value was
// renamed rather than aliased, deliberately, because an ownership mode is not
// something a config may leave to whatever the host happens to offer.
func TestIsContainerRuntime(t *testing.T) {
	for _, v := range []string{string(RuntimeContainerRootless), string(RuntimeContainerRootful)} {
		assert.True(t, IsContainerRuntime(v), "%q is a container runtime", v)
	}
	for _, v := range []string{"", "host", "container", "Container-Rootless", "rootless"} {
		assert.False(t, IsContainerRuntime(v), "%q is not a container runtime", v)
	}
}

// TestACPTransport_RequireOnHost_NativeAndBespokeNeverGate pins that a
// native (ACPNative) or bespoke (ACPBespoke) transport is never gated by
// this check regardless of PATH — there is no adapter binary to look up.
func TestACPTransport_RequireOnHost_NativeAndBespokeNeverGate(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	assert.NoError(t, ACPTransport{Kind: ACPNative}.RequireOnHost("", "kiro"))
	assert.NoError(t, ACPTransport{Kind: ACPBespoke}.RequireOnHost("", "testengine"))
}

// TestChatEvent_ExactlyOneVariant documents that a ChatEvent carries exactly one
// of Entry / Complete / Session.
func TestChatEvent_ExactlyOneVariant(t *testing.T) {
	for _, ev := range []ChatEvent{
		{Entry: &SessionEntry{}},
		{Complete: &TurnMeta{}},
		{Session: &ChatSessionInfo{}},
	} {
		set := 0
		if ev.Entry != nil {
			set++
		}
		if ev.Complete != nil {
			set++
		}
		if ev.Session != nil {
			set++
		}
		assert.Equal(t, 1, set, "exactly one ChatEvent variant must be set")
	}
}

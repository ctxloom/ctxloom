package grpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// TestChatMCPServer_ProtoRoundTrip_HttpSse proves the host<->plugin gRPC relay
// (B3, gap G11) carries an editor-supplied http/sse MCP server LOSSLESSLY —
// Transport/URL/Headers survive chatStartToProto -> chatStartFromProto byte
// for byte. Without this, an http/sse server that reaches ctxloom's ACP agent
// merge would silently vanish the moment a chat crossed this relay boundary
// (host process <-> plugin subprocess) — the exact silent-no-op shape this
// codebase must never ship.
func TestChatMCPServer_ProtoRoundTrip_HttpSse(t *testing.T) {
	req := agent.ChatRequest{
		MCPServers: []agent.ChatMCPServer{
			{Name: "stdio-tool", Command: "/bin/tools", Args: []string{"serve"}, Env: map[string]string{"K": "v"}},
			{Name: "remote-http", Transport: agent.MCPTransportHTTP, URL: "https://example.com/mcp", Headers: map[string]string{"Authorization": "Bearer tok"}},
			{Name: "remote-sse", Transport: agent.MCPTransportSSE, URL: "https://example.com/sse"},
		},
	}

	proto := chatStartToProto(req)
	require.Len(t, proto.GetMcpServers(), 3)
	assert.Equal(t, "http", proto.GetMcpServers()[1].GetTransport())
	assert.Equal(t, "https://example.com/mcp", proto.GetMcpServers()[1].GetUrl())
	assert.Equal(t, map[string]string{"Authorization": "Bearer tok"}, proto.GetMcpServers()[1].GetHeaders())
	assert.Equal(t, "sse", proto.GetMcpServers()[2].GetTransport())

	back, err := chatStartFromProto(proto)
	require.NoError(t, err)
	assert.Equal(t, req.MCPServers, back.MCPServers, "http/sse Transport/URL/Headers must survive the relay round trip byte for byte")
}

// TestTurnMetaToProto_SaturatesInsteadOfWrapping pins that every int field
// of agent.TurnMeta was narrowed to the proto's int32 with an unchecked
// conversion, so a value past the field's range WRAPPED — a token count over
// 2.1e9, or a turn longer than ~24.9 days, arrived as a small or negative
// number that reads as a perfectly plausible measurement. Saturating is not
// accurate either, but it cannot be mistaken for a real reading and it never
// changes sign.
func TestTurnMetaToProto_SaturatesInsteadOfWrapping(t *testing.T) {
	if math.MaxInt == math.MaxInt32 {
		t.Skip("int is 32 bits here: these values cannot be represented on the host side at all")
	}
	over := int(math.MaxInt32) + 1
	under := int(math.MinInt32) - 1

	got := turnMetaToProto(&agent.TurnMeta{
		InputTokens:         over,
		OutputTokens:        over,
		CacheReadTokens:     over,
		CacheCreationTokens: over,
		ContextWindow:       over,
		MaxOutputTokens:     over,
		DurationMs:          over,
		NumTurns:            under,
	})

	assert.Equal(t, int32(math.MaxInt32), got.GetInputTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetOutputTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetCacheReadTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetCacheCreationTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetContextWindow())
	assert.Equal(t, int32(math.MaxInt32), got.GetMaxOutputTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetDurationMs(),
		"a duration past ~24.9 days must not come back as a short one")
	assert.Equal(t, int32(math.MinInt32), got.GetNumTurns())
}

// TestTurnMetaToProto_InRangeValuesAreUntouched is the other half: clamping
// must not perturb any value the field can actually hold.
func TestTurnMetaToProto_InRangeValuesAreUntouched(t *testing.T) {
	got := turnMetaToProto(&agent.TurnMeta{
		InputTokens:   1234,
		DurationMs:    math.MaxInt32,
		NumTurns:      -7,
		ContextWindow: 0,
	})
	assert.Equal(t, int32(1234), got.GetInputTokens())
	assert.Equal(t, int32(math.MaxInt32), got.GetDurationMs())
	assert.Equal(t, int32(-7), got.GetNumTurns())
	assert.Equal(t, int32(0), got.GetContextWindow())
}

// chatBackend is a fakeBackend that also implements agent.StructuredChat: it
// echoes each inbound message as an assistant entry + a completion, framed by a
// session event.
type chatBackend struct {
	fakeBackend
	mu          sync.Mutex
	gotMessages []string
}

func (b *chatBackend) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	out <- agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model}}
	for msg := range in {
		b.mu.Lock()
		b.gotMessages = append(b.gotMessages, msg.Text)
		b.mu.Unlock()
		out <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "got: " + msg.Text}}
		out <- agent.ChatEvent{Complete: &agent.TurnMeta{InputTokens: len(msg.Text)}}
	}
	return nil
}

var _ agent.StructuredChat = (*chatBackend)(nil)

// fakeChatServerStream implements the bidi server stream for GRPCServer.Chat.
type fakeChatServerStream struct {
	ctx     context.Context
	recv    []*ChatInput
	recvIdx int
	recvErr error // returned once the canned inputs run out, instead of io.EOF
	sendErr error // non-nil: every Send fails (the client went away)
	mu      sync.Mutex
	sent    []*ChatEvent
}

func (s *fakeChatServerStream) Recv() (*ChatInput, error) {
	if s.recvIdx >= len(s.recv) {
		if s.recvErr != nil {
			return nil, s.recvErr
		}
		return nil, io.EOF
	}
	in := s.recv[s.recvIdx]
	s.recvIdx++
	return in, nil
}
func (s *fakeChatServerStream) Send(ev *ChatEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sendErr != nil {
		return s.sendErr
	}
	s.sent = append(s.sent, ev)
	return nil
}
func (s *fakeChatServerStream) sentEvents() []*ChatEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*ChatEvent(nil), s.sent...)
}
func (s *fakeChatServerStream) Context() context.Context     { return s.ctx }
func (s *fakeChatServerStream) SetHeader(metadata.MD) error  { return nil }
func (s *fakeChatServerStream) SendHeader(metadata.MD) error { return nil }
func (s *fakeChatServerStream) SetTrailer(metadata.MD)       {}
func (s *fakeChatServerStream) SendMsg(m any) error          { return s.Send(m.(*ChatEvent)) }
func (s *fakeChatServerStream) RecvMsg(m any) error          { return io.EOF }

var _ googlegrpc.BidiStreamingServer[ChatInput, ChatEvent] = (*fakeChatServerStream)(nil)

func TestGRPCServer_Chat_DrivesBackendAndStreamsEvents(t *testing.T) {
	backend := &chatBackend{fakeBackend: fakeBackend{name: "claude-code"}}
	srv := &GRPCServer{Impl: backend}
	stream := &fakeChatServerStream{
		ctx: context.Background(),
		recv: []*ChatInput{
			{Input: &ChatInput_Start{Start: &ChatStart{Model: "m"}}},
			{Input: &ChatInput_UserMessage{UserMessage: &ChatUserMessage{Text: "hello"}}},
		},
	}

	require.NoError(t, srv.Chat(stream))

	assert.Equal(t, []string{"hello"}, backend.gotMessages)
	evs := stream.sentEvents()
	require.Len(t, evs, 3)
	assert.NotNil(t, evs[0].GetSession())
	assert.Equal(t, "got: hello", evs[1].GetEntry().GetContent())
	require.NotNil(t, evs[2].GetComplete())
	assert.Equal(t, int32(5), evs[2].GetComplete().GetInputTokens())
}

func TestGRPCServer_Chat_UnimplementedWhenBackendLacksCapability(t *testing.T) {
	// fakeBackend does NOT implement agent.StructuredChat.
	srv := &GRPCServer{Impl: &fakeBackend{name: "antigravity"}}
	stream := &fakeChatServerStream{
		ctx:  context.Background(),
		recv: []*ChatInput{{Input: &ChatInput_Start{Start: &ChatStart{}}}},
	}

	err := srv.Chat(stream)
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))
}

// TestGRPCServer_Chat_FirstMessageMustBeStart pins the original rejection
// alongside its own: a first frame that carries no start is the CLIENT's
// protocol violation, and a bare fmt.Errorf reaches the client as
// codes.Unknown — indistinguishable from "the transport died". The same
// function already proves the right shape three lines down, where a backend
// without the capability returns codes.Unimplemented.
func TestGRPCServer_Chat_FirstMessageMustBeStart(t *testing.T) {
	srv := &GRPCServer{Impl: &chatBackend{fakeBackend: fakeBackend{name: "claude-code"}}}
	stream := &fakeChatServerStream{
		ctx:  context.Background(),
		recv: []*ChatInput{{Input: &ChatInput_UserMessage{UserMessage: &ChatUserMessage{Text: "no start"}}}},
	}
	err := srv.Chat(stream)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestGRPCServer_Chat_ClosedBeforeStartIsInvalidArgument covers the other
// protocol violation on the same frame: the client half-closed without ever
// sending a start. That is a malformed conversation, not a transport failure.
func TestGRPCServer_Chat_ClosedBeforeStartIsInvalidArgument(t *testing.T) {
	srv := &GRPCServer{Impl: &chatBackend{fakeBackend: fakeBackend{name: "claude-code"}}}
	stream := &fakeChatServerStream{ctx: context.Background()}

	err := srv.Chat(stream)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ctxObliviousChatBackend writes a fixed number of events to `out` with a plain
// channel send — no select on ctx. A backend is allowed to be written this way
// (the capability contract never promised otherwise), and a real one that
// streams a burst of tokens between ctx checks behaves identically for the
// duration of the burst.
type ctxObliviousChatBackend struct {
	fakeBackend
	events   int
	finished chan struct{}
}

func (b *ctxObliviousChatBackend) Chat(_ context.Context, _ agent.ChatRequest, _ <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(b.finished)
	defer close(out)
	for i := 0; i < b.events; i++ {
		out <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "token"}}
	}
	return nil
}

var _ agent.StructuredChat = (*ctxObliviousChatBackend)(nil)

// TestGRPCServer_Chat_SendFailureDrainsBackendOutput pins that when the
// client went away, the server returned from its `for ev := range out` loop
// immediately and never read `out` again. A backend mid-burst was left blocked
// on a channel send forever — a leaked goroutine holding the whole engine
// session, per abandoned chat. The handler must keep draining until the backend
// closes `out`.
func TestGRPCServer_Chat_SendFailureDrainsBackendOutput(t *testing.T) {
	backend := &ctxObliviousChatBackend{
		fakeBackend: fakeBackend{name: "claude-code"},
		events:      8,
		finished:    make(chan struct{}),
	}
	srv := &GRPCServer{Impl: backend}
	stream := &fakeChatServerStream{
		ctx:     context.Background(),
		recv:    []*ChatInput{{Input: &ChatInput_Start{Start: &ChatStart{}}}},
		sendErr: errors.New("client went away"),
	}

	err := srv.Chat(stream)
	require.Error(t, err, "the send failure is still the handler's error")

	select {
	case <-backend.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend's Chat never returned: its output channel was abandoned with a producer still blocked on it")
	}
}

// TestGRPCServer_Chat_RecvTransportErrorKeepsItsCode is the complement: when
// the failure really IS the transport, its status code must survive to the
// client rather than being flattened into Unknown by an fmt.Errorf wrap.
func TestGRPCServer_Chat_RecvTransportErrorKeepsItsCode(t *testing.T) {
	srv := &GRPCServer{Impl: &chatBackend{fakeBackend: fakeBackend{name: "claude-code"}}}
	stream := &fakeChatServerStream{
		ctx:     context.Background(),
		recvErr: status.Error(codes.Canceled, "client went away"),
	}

	err := srv.Chat(stream)
	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.Contains(t, err.Error(), "client went away")
}

// fakeChatClientStream implements the bidi client stream for GRPCClient.Chat.
//
// closeSendDone is closed inside CloseSend, which GRPCClient.Chat's outbound
// pump goroutine only calls once its `for msg := range in { stream.Send(msg) }`
// loop has fully drained — i.e. once every queued Send has already landed in
// `sent`. Callers that need `sentInputs` to reflect all messages a test wrote
// to `in` MUST wait on closeSendDone first: draining `events`/`errs` gives no
// such guarantee, since the canned recv data is independent of the outbound
// side and the two pump goroutines are otherwise unsynchronized.
type fakeChatClientStream struct {
	mu            sync.Mutex
	sent          []*ChatInput
	sendErrAfter  int // >0: fail every Send once this many have landed
	recv          []*ChatEvent
	recvIdx       int
	closeSendOnce sync.Once
	closeSendDone chan struct{}
}

func newFakeChatClientStream(recv []*ChatEvent) *fakeChatClientStream {
	return &fakeChatClientStream{recv: recv, closeSendDone: make(chan struct{})}
}

func (s *fakeChatClientStream) Send(in *ChatInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// sendErrAfter models a stream that breaks mid-conversation: the first N
	// sends land, every later one fails (and delivers nothing).
	if s.sendErrAfter > 0 && len(s.sent) >= s.sendErrAfter {
		return errors.New("chat stream send failed")
	}
	s.sent = append(s.sent, in)
	return nil
}
func (s *fakeChatClientStream) sentInputs() []*ChatInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*ChatInput(nil), s.sent...)
}
func (s *fakeChatClientStream) Recv() (*ChatEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recvIdx >= len(s.recv) {
		return nil, io.EOF
	}
	ev := s.recv[s.recvIdx]
	s.recvIdx++
	return ev, nil
}
func (s *fakeChatClientStream) CloseSend() error {
	s.closeSendOnce.Do(func() { close(s.closeSendDone) })
	return nil
}
func (s *fakeChatClientStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeChatClientStream) Trailer() metadata.MD         { return nil }
func (s *fakeChatClientStream) Context() context.Context     { return context.Background() }
func (s *fakeChatClientStream) SendMsg(any) error            { return nil }
func (s *fakeChatClientStream) RecvMsg(any) error            { return nil }

var _ googlegrpc.BidiStreamingClient[ChatInput, ChatEvent] = (*fakeChatClientStream)(nil)

func TestGRPCClient_Chat_SendsStartAndMessages_ReceivesEvents(t *testing.T) {
	cs := newFakeChatClientStream([]*ChatEvent{
		{Event: &ChatEvent_Session{Session: &ChatSessionInfo{Model: "m"}}},
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Content: "hi"}}},
		{Event: &ChatEvent_Complete{Complete: &TurnMeta{InputTokens: 5}}},
	})
	c := &GRPCClient{client: &fakeLLMClient{chatStream: cs}}

	in, events, errs, err := c.Chat(context.Background(), agent.ChatRequest{Model: "m"})
	require.NoError(t, err)

	in <- agent.ChatMessage{Text: "hello"}
	close(in)

	// The outbound pump only calls CloseSend once its loop over `in` has fully
	// drained (see fakeChatClientStream doc comment) — waiting here is what
	// actually guarantees "hello" has landed in `sent` below, rather than
	// relying on the unrelated timing of the events/errs drain.
	<-cs.closeSendDone

	var got []agent.ChatEvent
	for ev := range events {
		got = append(got, ev)
	}
	require.NoError(t, <-errs)

	require.Len(t, got, 3)
	require.NotNil(t, got[0].Session)
	assert.Equal(t, "m", got[0].Session.Model)
	require.NotNil(t, got[1].Entry)
	assert.Equal(t, "hi", got[1].Entry.Content)
	require.NotNil(t, got[2].Complete)

	// The stream carried the start then the user message.
	sent := cs.sentInputs()
	require.NotEmpty(t, sent)
	assert.NotNil(t, sent[0].GetStart())
	var texts []string
	for _, in := range sent {
		if um := in.GetUserMessage(); um != nil {
			texts = append(texts, um.GetText())
		}
	}
	assert.Equal(t, []string{"hello"}, texts)
}

// TestGRPCClient_Chat_SendFailureKeepsDrainingInput pins that when a
// stream Send failed, the inbound pump broke out of its loop and returned,
// leaving `in` with NO reader at all. Every later write to `in` — the channel
// this method hands the caller and documents as "write messages here" — blocked
// forever, with no error and no closed channel to notice. The pump must keep
// draining input after a send failure so the caller can still make progress and
// close `in` normally.
func TestGRPCClient_Chat_SendFailureKeepsDrainingInput(t *testing.T) {
	cs := newFakeChatClientStream(nil)
	cs.sendErrAfter = 1 // the start lands; every user message fails
	c := &GRPCClient{client: &fakeLLMClient{chatStream: cs}}

	in, events, errs, err := c.Chat(context.Background(), agent.ChatRequest{})
	require.NoError(t, err)

	go func() {
		for range events {
		}
		for range errs {
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		in <- agent.ChatMessage{Text: "first"}
		in <- agent.ChatMessage{Text: "second — this one used to block forever"}
		close(in)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writing to `in` blocked: the inbound pump abandoned the channel after a send failure")
	}

	select {
	case <-cs.closeSendDone:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseSend never fired — the pump did not run to completion")
	}

	// Nothing after the start was delivered, and nothing pretends otherwise.
	sent := cs.sentInputs()
	require.Len(t, sent, 1)
	assert.NotNil(t, sent[0].GetStart())
}

func TestGRPCClient_Chat_DialErrorReturned(t *testing.T) {
	c := &GRPCClient{client: &fakeLLMClient{chatErr: errors.New("dial failed")}}
	_, _, _, err := c.Chat(context.Background(), agent.ChatRequest{})
	require.Error(t, err)
}

// readCanonicalTranscript reads back harp's canonical transcript file
// (paths.HarpCanonicalTranscriptPath) into transcript.Record values, in file
// order. Record's fields are exported (unlike internal/transcript's own test
// helper), so this package reads the file directly rather than importing a
// test-only helper.
func readCanonicalTranscript(t *testing.T, harp string) []transcript.Record {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()

	var recs []transcript.Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r transcript.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		recs = append(recs, r)
	}
	require.NoError(t, scanner.Err())
	return recs
}

// TestGRPCClient_Chat_CapturesTranscriptWhenHarpPresent pins the
// S2 seam: when the caller stamps req.Env[agent.SessionHarpEnv] (exactly what
// acp_cmd.go does today), GRPCClient.Chat must (a) forward every event on the
// returned channel UNCHANGED (lossless passthrough) and (b) ALSO have written
// the same events to the harp's canonical transcript.jsonl, keyed by the
// engine name the plugin's Info RPC reports.
func TestGRPCClient_Chat_CapturesTranscriptWhenHarpPresent(t *testing.T) {
	testsupport.Isolate(t)
	harp := "grpc-chat-tee-harp"

	cs := newFakeChatClientStream([]*ChatEvent{
		{Event: &ChatEvent_Session{Session: &ChatSessionInfo{Model: "m"}}},
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Type: "assistant", Content: "hi there"}}},
		{Event: &ChatEvent_Complete{Complete: &TurnMeta{InputTokens: 5}}},
	})
	c := &GRPCClient{client: &fakeLLMClient{
		chatStream: cs,
		infoResp:   &LLMInfo{Name: "codex"},
	}}

	req := agent.ChatRequest{Model: "m", Env: map[string]string{agent.SessionHarpEnv: harp}}
	in, events, errs, err := c.Chat(context.Background(), req)
	require.NoError(t, err)
	close(in)

	var forwarded []agent.ChatEvent
	for ev := range events {
		forwarded = append(forwarded, ev)
	}
	require.NoError(t, <-errs)

	// (a) passthrough is lossless.
	require.Len(t, forwarded, 3)
	require.NotNil(t, forwarded[0].Session)
	assert.Equal(t, "m", forwarded[0].Session.Model)
	require.NotNil(t, forwarded[1].Entry)
	assert.Equal(t, "hi there", forwarded[1].Entry.Content)
	require.NotNil(t, forwarded[2].Complete)
	assert.Equal(t, 5, forwarded[2].Complete.InputTokens)

	// (b) every event was ALSO recorded to the canonical transcript, on a
	// valid-JSONL file, engine/harp stamped from the seam's own resolution.
	recs := readCanonicalTranscript(t, harp)
	require.Len(t, recs, 3)
	assert.Equal(t, transcript.KindSession, recs[0].Kind)
	assert.Equal(t, "codex", recs[0].Engine)
	assert.Equal(t, harp, recs[0].Harp)
	require.NotNil(t, recs[1].Entry)
	assert.Equal(t, "hi there", recs[1].Entry.Content)
	assert.Equal(t, transcript.KindComplete, recs[2].Kind)
}

// TestGRPCClient_Chat_CapturesUserTurn pins that the canonical
// transcript must carry the USER's own turns, not just the backend's
// outbound events. Tee only ever wraps the OUTBOUND events channel, so
// without a tap on the INBOUND `in` channel a structured session's
// transcript.jsonl had assistant output but no `user` entries at all.
func TestGRPCClient_Chat_CapturesUserTurn(t *testing.T) {
	testsupport.Isolate(t)
	harp := "grpc-chat-tee-user-turn-harp"

	cs := newFakeChatClientStream([]*ChatEvent{
		{Event: &ChatEvent_Session{Session: &ChatSessionInfo{Model: "m"}}},
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Type: "assistant", Content: "hi there"}}},
	})
	c := &GRPCClient{client: &fakeLLMClient{
		chatStream: cs,
		infoResp:   &LLMInfo{Name: "codex"},
	}}

	req := agent.ChatRequest{Model: "m", Env: map[string]string{agent.SessionHarpEnv: harp}}
	in, events, errs, err := c.Chat(context.Background(), req)
	require.NoError(t, err)

	in <- agent.ChatMessage{Text: "what does this function do?"}
	close(in)
	<-cs.closeSendDone

	var forwarded []agent.ChatEvent
	for ev := range events {
		forwarded = append(forwarded, ev)
	}
	require.NoError(t, <-errs)

	// The user's own text must NEVER be echoed back through the outbound
	// events stream that drives the live display — only the backend's own
	// events (session + assistant entry here) may appear there.
	require.Len(t, forwarded, 2, "the user turn must not be double-rendered onto the live events stream")
	require.NotNil(t, forwarded[0].Session)
	require.NotNil(t, forwarded[1].Entry)
	assert.Equal(t, "hi there", forwarded[1].Entry.Content)

	// But the canonical transcript must carry it, as a `user` entry, with the
	// real prompt text — this is the whole point of the fix.
	//
	// Position is NOT asserted for the user entry relative to the Session
	// record: they are recorded from two independent goroutines (the inbound
	// `in`-channel tap vs. the outbound events tee) with no causal
	// relationship to each other in this fake-stream test (the canned Session
	// event is emitted independent of anything arriving on `in` — matching
	// the real ACP driver, which likewise emits Session before ever reading
	// its first inbound message, see internal/acp/session.go's Chat). Which
	// one lands at seq 0 is genuinely a race, and Session — envelope
	// metadata, not conversation content (transcript/history.go's
	// entriesFromRecord) — never becomes a Session.Entries item either way,
	// so the race has no observable effect on any reader. What IS causally
	// fixed, and asserted below: the assistant's reply always follows the
	// Session record (both flow through the one sequential outbound tee).
	recs := readCanonicalTranscript(t, harp)
	require.Len(t, recs, 3)
	for _, r := range recs {
		assert.Equal(t, harp, r.Harp)
		assert.Equal(t, "codex", r.Engine)
	}

	var userRec, sessionRec, assistantRec *transcript.Record
	for i := range recs {
		switch {
		case recs[i].Kind == transcript.KindSession:
			sessionRec = &recs[i]
		case recs[i].Entry != nil && recs[i].Entry.Type == "user":
			userRec = &recs[i]
		case recs[i].Entry != nil && recs[i].Entry.Type == "assistant":
			assistantRec = &recs[i]
		}
	}
	require.NotNil(t, userRec, "the user's turn must be recorded to the canonical transcript")
	assert.Equal(t, "what does this function do?", userRec.Entry.Content)
	require.NotNil(t, sessionRec)
	require.NotNil(t, assistantRec)
	assert.Equal(t, "hi there", assistantRec.Entry.Content)
	assert.Less(t, sessionRec.Seq, assistantRec.Seq, "the assistant reply must follow the session record (single sequential outbound tee)")
}

// TestGRPCClient_Chat_PermissionAndCancelMessagesNotRecordedAsUserTurns
// proves the inbound tap discriminates: a permission answer or a turn-cancel
// control message on `in` must never be recorded as a `user` conversation
// entry — only genuine text turns are.
func TestGRPCClient_Chat_PermissionAndCancelMessagesNotRecordedAsUserTurns(t *testing.T) {
	testsupport.Isolate(t)
	harp := "grpc-chat-tee-control-msgs-harp"

	cs := newFakeChatClientStream(nil)
	c := &GRPCClient{client: &fakeLLMClient{
		chatStream: cs,
		infoResp:   &LLMInfo{Name: "codex"},
	}}

	req := agent.ChatRequest{Model: "m", Env: map[string]string{agent.SessionHarpEnv: harp}}
	in, events, errs, err := c.Chat(context.Background(), req)
	require.NoError(t, err)

	in <- agent.ChatMessage{Permission: &agent.PermissionAnswer{ID: "p1", OptionID: "allow_once"}}
	in <- agent.ChatMessage{CancelTurn: true}
	close(in)
	<-cs.closeSendDone

	for range events {
	}
	require.NoError(t, <-errs)

	path, perr := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, perr)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "a permission answer / cancel-turn control message must never open (or write to) the transcript")
}

// TestGRPCClient_Chat_NoHarpSkipsCapture pins the documented S4 gap: with no
// harp on the request (run_structured.go mints none today), Chat must not
// attempt capture at all — no Info RPC, no transcript file — and must still
// forward every event unchanged. This is the "degrades gracefully, never
// crashes" contract for the no-harp case.
func TestGRPCClient_Chat_NoHarpSkipsCapture(t *testing.T) {
	testsupport.Isolate(t)

	cs := newFakeChatClientStream([]*ChatEvent{
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Type: "assistant", Content: "hi"}}},
	})
	fc := &fakeLLMClient{chatStream: cs}
	c := &GRPCClient{client: fc}

	in, events, errs, err := c.Chat(context.Background(), agent.ChatRequest{Model: "m"})
	require.NoError(t, err)
	close(in)

	var forwarded []agent.ChatEvent
	for ev := range events {
		forwarded = append(forwarded, ev)
	}
	require.NoError(t, <-errs)

	require.Len(t, forwarded, 1)
	assert.Equal(t, "hi", forwarded[0].Entry.Content)
	assert.Equal(t, 0, fc.gotInfoCalls, "no harp on the request must skip the Info RPC capture uses to resolve the engine name")
}

// TestGRPCClient_Chat_InfoFailureDegradesGracefully asserts that when a harp
// IS present but the plugin's Info RPC fails, capture is skipped rather than
// the chat itself failing or stalling — a transcript-capture fault must never
// be visible to the live chat it shadows.
func TestGRPCClient_Chat_InfoFailureDegradesGracefully(t *testing.T) {
	testsupport.Isolate(t)
	harp := "grpc-chat-tee-info-fail-harp"

	cs := newFakeChatClientStream([]*ChatEvent{
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Type: "assistant", Content: "hi"}}},
	})
	c := &GRPCClient{client: &fakeLLMClient{
		chatStream: cs,
		infoErr:    errors.New("info rpc unavailable"),
	}}

	req := agent.ChatRequest{Model: "m", Env: map[string]string{agent.SessionHarpEnv: harp}}
	in, events, errs, err := c.Chat(context.Background(), req)
	require.NoError(t, err)
	close(in)

	var forwarded []agent.ChatEvent
	for ev := range events {
		forwarded = append(forwarded, ev)
	}
	require.NoError(t, <-errs)
	require.Len(t, forwarded, 1)
	assert.Equal(t, "hi", forwarded[0].Entry.Content)

	path, perr := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, perr)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "an Info RPC failure must not leave a transcript file behind")
}

// TestGRPCClient_Chat_ConcurrentTurnsRecordAllWithoutGapsOrDuplicateSeq pins
// the regression coverage: many user turns fired concurrently
// (simulating a caller that doesn't wait for a reply before sending the
// next message, e.g. piped/pasted stdin in run_structured.go's
// readMessagesLoop) racing many backend events, must all land in the
// canonical transcript — none dropped, none double-recorded, and Seq must
// be a gapless, non-duplicated 0..N-1 run. Before the fix this
// held only because fileRecorder's own mutex happened to serialize writes;
// after the fix it holds because GRPCClient.Chat funnels both producers
// through a single transcript.CoordinatedRecorder owner — this test proves
// the payload survives that refactor intact under real concurrent load, not
// just the exit code.
func TestGRPCClient_Chat_ConcurrentTurnsRecordAllWithoutGapsOrDuplicateSeq(t *testing.T) {
	testsupport.Isolate(t)
	harp := "grpc-chat-concurrent-load-harp"

	const numEngineEvents = 40
	canned := make([]*ChatEvent, 0, numEngineEvents)
	for i := 0; i < numEngineEvents; i++ {
		canned = append(canned, &ChatEvent{Event: &ChatEvent_Entry{Entry: &SessionEntry{
			Type: "assistant", Content: "engine-event",
		}}})
	}
	cs := newFakeChatClientStream(canned)
	c := &GRPCClient{client: &fakeLLMClient{
		chatStream: cs,
		infoResp:   &LLMInfo{Name: "codex"},
	}}

	req := agent.ChatRequest{Model: "m", Env: map[string]string{agent.SessionHarpEnv: harp}}
	in, events, errs, err := c.Chat(context.Background(), req)
	require.NoError(t, err)

	const numUserTurns = 40
	var sendWG sync.WaitGroup
	sendWG.Add(1)
	go func() {
		defer sendWG.Done()
		for i := 0; i < numUserTurns; i++ {
			in <- agent.ChatMessage{Text: "user-turn"}
		}
		close(in)
	}()

	var forwarded int
	for range events {
		forwarded++
	}
	require.NoError(t, <-errs)
	sendWG.Wait()

	require.Equal(t, numEngineEvents, forwarded, "every backend event must still reach the live caller")

	recs := readCanonicalTranscript(t, harp)
	require.Len(t, recs, numEngineEvents+numUserTurns,
		"canonical transcript must carry every engine event AND every user turn under concurrent load — no drops")

	seen := make(map[int]bool, len(recs))
	for _, r := range recs {
		assert.False(t, seen[r.Seq], "duplicate Seq %d in canonical transcript", r.Seq)
		seen[r.Seq] = true
	}
	for i := 0; i < len(recs); i++ {
		assert.True(t, seen[i], "Seq must be a gapless 0..N-1 run; missing Seq %d", i)
	}
}

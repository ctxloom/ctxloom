package grpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

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
	mu      sync.Mutex
	sent    []*ChatEvent
}

func (s *fakeChatServerStream) Recv() (*ChatInput, error) {
	if s.recvIdx >= len(s.recv) {
		return nil, io.EOF
	}
	in := s.recv[s.recvIdx]
	s.recvIdx++
	return in, nil
}
func (s *fakeChatServerStream) Send(ev *ChatEvent) error {
	s.mu.Lock()
	s.sent = append(s.sent, ev)
	s.mu.Unlock()
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

func TestGRPCServer_Chat_FirstMessageMustBeStart(t *testing.T) {
	srv := &GRPCServer{Impl: &chatBackend{fakeBackend: fakeBackend{name: "claude-code"}}}
	stream := &fakeChatServerStream{
		ctx:  context.Background(),
		recv: []*ChatInput{{Input: &ChatInput_UserMessage{UserMessage: &ChatUserMessage{Text: "no start"}}}},
	}
	require.Error(t, srv.Chat(stream))
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
	s.sent = append(s.sent, in)
	s.mu.Unlock()
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

// TestGRPCClient_Chat_CapturesTranscriptWhenHarpPresent pins the tough-cloud
// S2 seam: when the caller stamps req.Env[agent.SessionHarpEnv] (exactly what
// acp_cmd.go does today), GRPCClient.Chat must (a) forward every event on the
// returned channel UNCHANGED (lossless passthrough) and (b) ALSO have written
// the same events to the harp's canonical transcript.acp.jsonl, keyed by the
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

// TestGRPCClient_Chat_CapturesUserTurn pins edgy-ivory: the canonical
// transcript must carry the USER's own turns, not just the backend's
// outbound events. Tee only ever wraps the OUTBOUND events channel, so
// without a tap on the INBOUND `in` channel a structured session's
// transcript.acp.jsonl had assistant output but no `user` entries at all.
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
	recs := readCanonicalTranscript(t, harp)
	require.Len(t, recs, 3)
	require.NotNil(t, recs[0].Entry, "the user's turn must be recorded to the canonical transcript")
	assert.Equal(t, "user", recs[0].Entry.Type)
	assert.Equal(t, "what does this function do?", recs[0].Entry.Content)
	assert.Equal(t, harp, recs[0].Harp)
	assert.Equal(t, "codex", recs[0].Engine)

	assert.Equal(t, transcript.KindSession, recs[1].Kind)
	require.NotNil(t, recs[2].Entry)
	assert.Equal(t, "hi there", recs[2].Entry.Content)
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

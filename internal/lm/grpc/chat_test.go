package grpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
type fakeChatClientStream struct {
	mu      sync.Mutex
	sent    []*ChatInput
	recv    []*ChatEvent
	recvIdx int
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
func (s *fakeChatClientStream) CloseSend() error             { return nil }
func (s *fakeChatClientStream) Header() (metadata.MD, error) { return nil, nil }
func (s *fakeChatClientStream) Trailer() metadata.MD         { return nil }
func (s *fakeChatClientStream) Context() context.Context     { return context.Background() }
func (s *fakeChatClientStream) SendMsg(any) error            { return nil }
func (s *fakeChatClientStream) RecvMsg(any) error            { return nil }

var _ googlegrpc.BidiStreamingClient[ChatInput, ChatEvent] = (*fakeChatClientStream)(nil)

func TestGRPCClient_Chat_SendsStartAndMessages_ReceivesEvents(t *testing.T) {
	cs := &fakeChatClientStream{recv: []*ChatEvent{
		{Event: &ChatEvent_Session{Session: &ChatSessionInfo{Model: "m"}}},
		{Event: &ChatEvent_Entry{Entry: &SessionEntry{Content: "hi"}}},
		{Event: &ChatEvent_Complete{Complete: &TurnMeta{InputTokens: 5}}},
	}}
	c := &GRPCClient{client: &fakeLLMClient{chatStream: cs}}

	in, events, errs, err := c.Chat(context.Background(), agent.ChatRequest{Model: "m"})
	require.NoError(t, err)

	in <- "hello"
	close(in)

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

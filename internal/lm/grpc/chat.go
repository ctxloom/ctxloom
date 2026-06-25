package grpc

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/shared/agent"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file carries the structured-chat transport: a bidirectional stream that
// drives a backend's StructuredChat capability (claude-code's stream-json mode)
// — user messages in, normalized turn events out, no pty. The capability is
// OPTIONAL: the server type-asserts it and returns UNIMPLEMENTED when absent.

// --- conversions (agent <-> proto) ---

func chatStartToProto(req agent.ChatRequest) *ChatStart {
	return &ChatStart{
		WorkDir:     req.WorkDir,
		Model:       req.Model,
		Env:         req.Env,
		AutoApprove: req.AutoApprove,
	}
}

func chatStartFromProto(p *ChatStart) agent.ChatRequest {
	return agent.ChatRequest{
		WorkDir:     p.GetWorkDir(),
		Model:       p.GetModel(),
		Env:         p.GetEnv(),
		AutoApprove: p.GetAutoApprove(),
	}
}

func chatEventToProto(ev agent.ChatEvent) *ChatEvent {
	switch {
	case ev.Entry != nil:
		return &ChatEvent{Event: &ChatEvent_Entry{Entry: entryToProto(*ev.Entry)}}
	case ev.Complete != nil:
		return &ChatEvent{Event: &ChatEvent_Complete{Complete: turnMetaToProto(ev.Complete)}}
	case ev.Session != nil:
		return &ChatEvent{Event: &ChatEvent_Session{Session: chatSessionInfoToProto(ev.Session)}}
	default:
		return &ChatEvent{}
	}
}

func chatEventFromProto(p *ChatEvent) agent.ChatEvent {
	switch ev := p.GetEvent().(type) {
	case *ChatEvent_Entry:
		e := entryFromProto(ev.Entry)
		return agent.ChatEvent{Entry: &e}
	case *ChatEvent_Complete:
		return agent.ChatEvent{Complete: turnMetaFromProto(ev.Complete)}
	case *ChatEvent_Session:
		return agent.ChatEvent{Session: chatSessionInfoFromProto(ev.Session)}
	default:
		return agent.ChatEvent{}
	}
}

func turnMetaToProto(m *agent.TurnMeta) *TurnMeta {
	if m == nil {
		return nil
	}
	return &TurnMeta{
		InputTokens:         int32(m.InputTokens),
		OutputTokens:        int32(m.OutputTokens),
		CacheReadTokens:     int32(m.CacheReadTokens),
		CacheCreationTokens: int32(m.CacheCreationTokens),
		ContextWindow:       int32(m.ContextWindow),
		MaxOutputTokens:     int32(m.MaxOutputTokens),
		CostUsd:             m.CostUSD,
		Model:               m.Model,
		StopReason:          m.StopReason,
		DurationMs:          int32(m.DurationMs),
		NumTurns:            int32(m.NumTurns),
	}
}

func turnMetaFromProto(p *TurnMeta) *agent.TurnMeta {
	if p == nil {
		return nil
	}
	return &agent.TurnMeta{
		InputTokens:         int(p.GetInputTokens()),
		OutputTokens:        int(p.GetOutputTokens()),
		CacheReadTokens:     int(p.GetCacheReadTokens()),
		CacheCreationTokens: int(p.GetCacheCreationTokens()),
		ContextWindow:       int(p.GetContextWindow()),
		MaxOutputTokens:     int(p.GetMaxOutputTokens()),
		CostUSD:             p.GetCostUsd(),
		Model:               p.GetModel(),
		StopReason:          p.GetStopReason(),
		DurationMs:          int(p.GetDurationMs()),
		NumTurns:            int(p.GetNumTurns()),
	}
}

func chatSessionInfoToProto(s *agent.ChatSessionInfo) *ChatSessionInfo {
	if s == nil {
		return nil
	}
	out := &ChatSessionInfo{
		Model:          s.Model,
		PermissionMode: s.PermissionMode,
		ContextWindow:  int32(s.ContextWindow),
	}
	for _, m := range s.MCPServers {
		out.McpServers = append(out.McpServers, &MCPStatus{Name: m.Name, Status: m.Status})
	}
	return out
}

func chatSessionInfoFromProto(p *ChatSessionInfo) *agent.ChatSessionInfo {
	if p == nil {
		return nil
	}
	out := &agent.ChatSessionInfo{
		Model:          p.GetModel(),
		PermissionMode: p.GetPermissionMode(),
		ContextWindow:  int(p.GetContextWindow()),
	}
	for _, m := range p.GetMcpServers() {
		out.MCPServers = append(out.MCPServers, agent.MCPStatus{Name: m.GetName(), Status: m.GetStatus()})
	}
	return out
}

// --- server (plugin) handler ---

// Chat bridges the bidirectional gRPC stream to the backend's StructuredChat
// capability. UNIMPLEMENTED when the backend lacks it (polymorphic: only agents
// with a native programmatic protocol implement it). The first input must carry
// `start`; subsequent `user_message`s feed the backend's input channel, and its
// normalized events stream back as ChatEvents.
func (s *GRPCServer) Chat(stream LLM_ChatServer) error {
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive chat start: %w", err)
	}
	start := first.GetStart()
	if start == nil {
		return fmt.Errorf("first Chat message must carry start")
	}

	chat, ok := s.Impl.(agent.StructuredChat)
	if !ok {
		return status.Errorf(codes.Unimplemented, "backend %s does not support structured chat", s.Impl.Name())
	}

	ctx := stream.Context()
	in := make(chan agent.ChatMessage)
	out := make(chan agent.ChatEvent)

	// Pump inbound user messages until the client half-closes (Recv errors).
	go func() {
		defer close(in)
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				return // EOF (client done) or error → close in
			}
			if um := msg.GetUserMessage(); um != nil {
				select {
				case in <- agent.ChatMessage{Text: um.GetText()}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// Run the backend's chat; it closes `out` when done.
	chatErr := make(chan error, 1)
	go func() { chatErr <- chat.Chat(ctx, chatStartFromProto(start), in, out) }()

	for ev := range out {
		if serr := stream.Send(chatEventToProto(ev)); serr != nil {
			return serr // client gone; stream ctx cancels, chat unwinds
		}
	}
	return <-chatErr
}

// --- client (host) method ---

// Chat opens the bidirectional stream and exposes it as channels: write user
// message text to the returned `in` channel and CLOSE it to end input; read
// normalized events from `events` (closed on stream end / ctx cancel); a fatal
// receive error arrives on `errs`. Mirrors the agent.StructuredChat channel shape
// at the host boundary.
func (c *GRPCClient) Chat(ctx context.Context, req agent.ChatRequest) (chan<- string, <-chan agent.ChatEvent, <-chan error, error) {
	stream, err := c.client.Chat(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := stream.Send(&ChatInput{Input: &ChatInput_Start{Start: chatStartToProto(req)}}); err != nil {
		return nil, nil, nil, fmt.Errorf("send chat start: %w", err)
	}

	in := make(chan string)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)

	go func() {
		for text := range in {
			if serr := stream.Send(&ChatInput{Input: &ChatInput_UserMessage{UserMessage: &ChatUserMessage{Text: text}}}); serr != nil {
				break
			}
		}
		_ = stream.CloseSend() // input done → half-close so the backend completes
	}()

	go func() {
		defer close(events)
		defer close(errs)
		for {
			ev, rerr := stream.Recv()
			if rerr == io.EOF {
				return
			}
			if rerr != nil {
				errs <- rerr
				return
			}
			select {
			case events <- chatEventFromProto(ev):
			case <-ctx.Done():
				return
			}
		}
	}()

	return in, events, errs, nil
}

// Chat delegates to the underlying gRPC client.
func (p *LLMRunner) Chat(ctx context.Context, req agent.ChatRequest) (chan<- string, <-chan agent.ChatEvent, <-chan error, error) {
	return p.grpc.Chat(ctx, req)
}

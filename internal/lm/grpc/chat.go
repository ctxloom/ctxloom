package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file carries the structured-chat transport: a bidirectional stream that
// drives a backend's StructuredChat capability (claude-code's stream-json mode)
// — user messages in, normalized turn events out, no pty. The capability is
// OPTIONAL: the server type-asserts it and returns UNIMPLEMENTED when absent.

// --- conversions (agent <-> proto) ---

func chatStartToProto(req agent.ChatRequest) *ChatStart {
	out := &ChatStart{
		WorkDir:             req.WorkDir,
		Model:               req.Model,
		Env:                 req.Env,
		PermissionMode:      req.Permissions.String(),
		ForwardPermissions:  req.ForwardPermissions,
		TranscriptRawPolicy: req.TranscriptRawPolicy,
		ForwardTerminal:     req.ForwardTerminal,
	}
	for _, m := range req.MCPServers {
		out.McpServers = append(out.McpServers, &ChatMCPServer{
			Name:      m.Name,
			Command:   m.Command,
			Args:      m.Args,
			Env:       m.Env,
			Transport: string(m.Transport),
			Url:       m.URL,
			Headers:   m.Headers,
		})
	}
	return out
}

func chatStartFromProto(p *ChatStart) agent.ChatRequest {
	req := agent.ChatRequest{
		WorkDir:             p.GetWorkDir(),
		Model:               p.GetModel(),
		Env:                 p.GetEnv(),
		Permissions:         agent.WireMode(p.GetPermissionMode()),
		ForwardPermissions:  p.GetForwardPermissions(),
		TranscriptRawPolicy: p.GetTranscriptRawPolicy(),
		ForwardTerminal:     p.GetForwardTerminal(),
	}
	for _, m := range p.GetMcpServers() {
		req.MCPServers = append(req.MCPServers, agent.ChatMCPServer{
			Name:      m.GetName(),
			Command:   m.GetCommand(),
			Args:      m.GetArgs(),
			Env:       m.GetEnv(),
			Transport: agent.MCPTransport(m.GetTransport()),
			URL:       m.GetUrl(),
			Headers:   m.GetHeaders(),
		})
	}
	return req
}

func chatEventToProto(ev agent.ChatEvent) *ChatEvent {
	out := &ChatEvent{Raw: ev.Raw}
	switch {
	case ev.Entry != nil:
		out.Event = &ChatEvent_Entry{Entry: EntryToProto(*ev.Entry)}
	case ev.Complete != nil:
		out.Event = &ChatEvent_Complete{Complete: turnMetaToProto(ev.Complete)}
	case ev.Session != nil:
		out.Event = &ChatEvent_Session{Session: chatSessionInfoToProto(ev.Session)}
	case ev.Permission != nil:
		out.Event = &ChatEvent_Permission{Permission: permissionRequestToProto(ev.Permission)}
	case ev.Terminal != nil:
		out.Event = &ChatEvent_Terminal{Terminal: terminalRequestToProto(ev.Terminal)}
	}
	return out
}

func chatEventFromProto(p *ChatEvent) agent.ChatEvent {
	raw := json.RawMessage(p.GetRaw())
	if len(raw) == 0 {
		raw = nil
	}
	switch ev := p.GetEvent().(type) {
	case *ChatEvent_Entry:
		e := entryFromProto(ev.Entry)
		return agent.ChatEvent{Entry: &e, Raw: raw}
	case *ChatEvent_Complete:
		return agent.ChatEvent{Complete: turnMetaFromProto(ev.Complete), Raw: raw}
	case *ChatEvent_Session:
		return agent.ChatEvent{Session: chatSessionInfoFromProto(ev.Session), Raw: raw}
	case *ChatEvent_Permission:
		return agent.ChatEvent{Permission: permissionRequestFromProto(ev.Permission), Raw: raw}
	case *ChatEvent_Terminal:
		return agent.ChatEvent{Terminal: terminalRequestFromProto(ev.Terminal), Raw: raw}
	default:
		return agent.ChatEvent{Raw: raw}
	}
}

func terminalRequestToProto(t *agent.TerminalRequest) *ChatTerminalRequest {
	if t == nil {
		return nil
	}
	return &ChatTerminalRequest{Id: t.ID, Op: t.Op, Params: t.Params}
}

func terminalRequestFromProto(p *ChatTerminalRequest) *agent.TerminalRequest {
	if p == nil {
		return nil
	}
	return &agent.TerminalRequest{ID: p.GetId(), Op: p.GetOp(), Params: p.GetParams()}
}

func terminalAnswerToProto(t *agent.TerminalResponse) *ChatTerminalAnswer {
	if t == nil {
		return nil
	}
	return &ChatTerminalAnswer{Id: t.ID, Result: t.Result, Error: t.Error}
}

func terminalAnswerFromProto(p *ChatTerminalAnswer) *agent.TerminalResponse {
	if p == nil {
		return nil
	}
	return &agent.TerminalResponse{ID: p.GetId(), Result: p.GetResult(), Error: p.GetError()}
}

func permissionRequestToProto(p *agent.PermissionRequest) *ChatPermissionRequest {
	if p == nil {
		return nil
	}
	out := &ChatPermissionRequest{
		Id:         p.ID,
		ToolName:   p.ToolName,
		ToolInput:  p.ToolInput,
		ToolCallId: p.ToolCallID,
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, &ChatPermissionOption{Id: o.ID, Kind: o.Kind, Name: o.Name})
	}
	return out
}

func permissionRequestFromProto(p *ChatPermissionRequest) *agent.PermissionRequest {
	if p == nil {
		return nil
	}
	out := &agent.PermissionRequest{
		ID:         p.GetId(),
		ToolName:   p.GetToolName(),
		ToolInput:  p.GetToolInput(),
		ToolCallID: p.GetToolCallId(),
	}
	for _, o := range p.GetOptions() {
		out.Options = append(out.Options, agent.PermissionOption{ID: o.GetId(), Kind: o.GetKind(), Name: o.GetName()})
	}
	return out
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

	// Pump inbound messages (user turns + permission answers + turn cancels)
	// until the client half-closes (Recv errors).
	go func() {
		defer close(in)
		for {
			msg, rerr := stream.Recv()
			if rerr != nil {
				return // EOF (client done) or error → close in
			}
			var cm agent.ChatMessage
			switch input := msg.GetInput().(type) {
			case *ChatInput_UserMessage:
				cm = agent.ChatMessage{
					Text:          input.UserMessage.GetText(),
					ContentBlocks: contentBlocksFromProto(input.UserMessage.GetContentBlocks()),
				}
			case *ChatInput_PermissionAnswer:
				cm = agent.ChatMessage{Permission: &agent.PermissionAnswer{
					ID:       input.PermissionAnswer.GetId(),
					OptionID: input.PermissionAnswer.GetOptionId(),
				}}
			case *ChatInput_CancelTurn:
				cm = agent.ChatMessage{CancelTurn: true}
			case *ChatInput_TerminalAnswer:
				cm = agent.ChatMessage{Terminal: terminalAnswerFromProto(input.TerminalAnswer)}
			default:
				continue // a stray start / unknown variant — ignore
			}
			select {
			case in <- cm:
			case <-ctx.Done():
				return
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

// Chat opens the bidirectional stream and exposes it as channels: write
// messages (user turns, permission answers, turn cancels) to the returned `in`
// channel and CLOSE it to end input; read normalized events from `events`
// (closed on stream end / ctx cancel); a fatal receive error arrives on `errs`.
// Mirrors the agent.StructuredChat channel shape at the host boundary.
func (c *GRPCClient) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	stream, err := c.client.Chat(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := stream.Send(&ChatInput{Input: &ChatInput_Start{Start: chatStartToProto(req)}}); err != nil {
		return nil, nil, nil, fmt.Errorf("send chat start: %w", err)
	}

	in := make(chan agent.ChatMessage)
	events := make(chan agent.ChatEvent)
	errs := make(chan error, 1)

	// Tough-cloud S2: capture this conversation's canonical transcript at the
	// host boundary — this is THE host seam behind `ctxloom run --structured`
	// and `ctxloom acp` (plan §2c). The harp rides req.Env[SessionHarpEnv]:
	// acp_cmd.go already stamps it there for the engine subprocess's own env,
	// and it is equally available here without any ChatRequest field addition.
	//
	// S4 verified this env-var plumbing rather than adding to it: run.go's
	// AssignSession (cmd/run.go, unconditional since well before S2/S4) mints
	// activeHarp and stamps runEnv["CTXLOOM_SESSION_HARP"] regardless of
	// --structured, and that env rides req.Options.Env straight into
	// runStructuredREPL's agent.ChatRequest — so req.Env[SessionHarpEnv] is
	// already populated on this path today. Confirmed end to end: a real
	// `ctxloom run --structured` session mints a harp and writes a non-empty
	// persist/transcript.acp.jsonl.
	//
	// edgy-ivory: the recorder is opened HERE, once, and shared by both the
	// inbound pump below (records the user's own turns — Tee only ever sees
	// the OUTBOUND events channel, so without this tap a structured
	// transcript carried assistant output but no `user` entries at all) and
	// the outbound tee (records everything else). One Recorder, one seq
	// counter, one file — a second NewRecorder here would race the first for
	// the file and reset Seq to 0.
	var rec transcript.Recorder
	if harp := req.Env[agent.SessionHarpEnv]; harp != "" {
		rec = c.openRecorder(ctx, harp, req.TranscriptRawPolicy)
	}

	go func() {
		for msg := range in {
			if msg.Permission == nil && msg.Terminal == nil && !msg.CancelTurn {
				transcript.RecordUserText(rec, msg.Text)
			}
			if serr := stream.Send(chatMessageToInput(msg)); serr != nil {
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

	outEvents := (<-chan agent.ChatEvent)(events)
	if rec != nil {
		outEvents = transcript.TeeAndClose(rec, events)
	}

	return in, outEvents, errs, nil
}

// openRecorder opens a transcript.Recorder for harp — keyed by the engine
// name the plugin's own Info RPC reports, so the recorder never has to guess
// or duplicate a backend-name lookup the caller already resolved elsewhere.
// Capture is best-effort end to end: an Info failure, an empty engine name,
// or a NewRecorder failure all log a warning and return nil. A transcript we
// failed to open is not a reason to fail, delay, or alter the chat it would
// have shadowed.
func (c *GRPCClient) openRecorder(ctx context.Context, harp, rawPolicy string) transcript.Recorder {
	info, err := c.client.Info(ctx, &Empty{})
	if err != nil {
		clidiag.Warn("ctxloom", "transcript capture: resolve engine name for harp %s: %v", harp, err)
		return nil
	}
	engine := info.GetName()
	if engine == "" {
		clidiag.Warn("ctxloom", "transcript capture: plugin reported no engine name for harp %s; skipping capture", harp)
		return nil
	}
	rec, err := transcript.NewRecorder(harp, engine, transcript.WithRawPolicy(transcript.RawPolicy(rawPolicy)))
	if err != nil {
		clidiag.Warn("ctxloom", "transcript capture: open recorder for harp %s (engine %s): %v", harp, engine, err)
		return nil
	}
	return rec
}

// chatMessageToInput maps one host-side chat message onto its ChatInput frame.
func chatMessageToInput(msg agent.ChatMessage) *ChatInput {
	switch {
	case msg.Permission != nil:
		return &ChatInput{Input: &ChatInput_PermissionAnswer{PermissionAnswer: &ChatPermissionAnswer{
			Id:       msg.Permission.ID,
			OptionId: msg.Permission.OptionID,
		}}}
	case msg.CancelTurn:
		return &ChatInput{Input: &ChatInput_CancelTurn{CancelTurn: &ChatCancelTurn{}}}
	case msg.Terminal != nil:
		return &ChatInput{Input: &ChatInput_TerminalAnswer{TerminalAnswer: terminalAnswerToProto(msg.Terminal)}}
	default:
		return &ChatInput{Input: &ChatInput_UserMessage{UserMessage: &ChatUserMessage{
			Text:          msg.Text,
			ContentBlocks: contentBlocksToProto(msg.ContentBlocks),
		}}}
	}
}

// Chat delegates to the underlying gRPC client.
func (p *LLMRunner) Chat(ctx context.Context, req agent.ChatRequest) (chan<- agent.ChatMessage, <-chan agent.ChatEvent, <-chan error, error) {
	return p.grpc.Chat(ctx, req)
}

package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file carries the structured-chat transport: a bidirectional stream that
// drives a backend's StructuredChat capability (claude-code rides the ACP adapter)
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
		Runtime:             req.Runtime,
		ResumeSessionId:     req.ResumeSessionID,
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
		Runtime:             p.GetRuntime(),
		ResumeSessionID:     p.GetResumeSessionId(),
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
		Kind:       p.Kind,
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
		Kind:       p.GetKind(),
	}
	for _, o := range p.GetOptions() {
		out.Options = append(out.Options, agent.PermissionOption{ID: o.GetId(), Kind: o.GetKind(), Name: o.GetName()})
	}
	return out
}

// clampInt32 narrows a host-side int onto the wire's int32 without wrapping.
// An unchecked conversion turns an out-of-range measurement into a small or
// negative one that reads as a plausible reading; saturating keeps the sign and
// puts the value at the edge of the range, where it is recognizable as a
// ceiling rather than a measurement.
func clampInt32(v int) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int32(v)
}

func turnMetaToProto(m *agent.TurnMeta) *TurnMeta {
	if m == nil {
		return nil
	}
	return &TurnMeta{
		InputTokens:         clampInt32(m.InputTokens),
		OutputTokens:        clampInt32(m.OutputTokens),
		CacheReadTokens:     clampInt32(m.CacheReadTokens),
		CacheCreationTokens: clampInt32(m.CacheCreationTokens),
		ContextWindow:       clampInt32(m.ContextWindow),
		MaxOutputTokens:     clampInt32(m.MaxOutputTokens),
		CostUsd:             m.CostUSD,
		Model:               m.Model,
		StopReason:          m.StopReason,
		DurationMs:          clampInt32(m.DurationMs),
		NumTurns:            clampInt32(m.NumTurns),
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
		SessionId:      s.SessionID,
		Resumable:      s.Resumable,
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
		SessionID:      p.GetSessionId(),
		Resumable:      p.GetResumable(),
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
		// A client that half-closes before sending anything violated the
		// protocol; anything else is the transport failing, and its own status
		// code is the one the client needs to see.
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "chat stream closed before the required start message")
		}
		return fmt.Errorf("receive chat start: %w", err)
	}
	start := first.GetStart()
	if start == nil {
		return status.Error(codes.InvalidArgument, "first Chat message must carry start")
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
			cm, ok := chatMessageFromInput(msg)
			if !ok {
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
			// Client gone; the stream ctx cancels and chat unwinds. Keep
			// consuming `out` until the backend closes it: a backend that
			// writes to `out` with a plain channel send (no ctx select — the
			// capability never required one) would otherwise stay blocked on
			// that send forever, leaking a goroutine and the whole engine
			// session it holds.
			go func() {
				for range out { //nolint:revive // draining, the values are unwanted
				}
			}()
			return serr
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
//
// When a transcript is being captured (req.Env[agent.SessionHarpEnv] set),
// `events` closing is also a completion barrier for that capture: the
// outbound tee delays closing `events` until transcript.CoordinatedRecorder's
// Done() fires, which requires every producer — inbound tap AND outbound tee
// — to have finished submitting and every submitted record to have actually
// been written. A caller may treat `events` closing as "the transcript, if
// any, is now durably complete," not merely "the backend stream ended."
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

	capture := c.openChatCapture(ctx, req)
	go pumpChatInput(ctx, stream, in, capture)
	go pumpChatEvents(ctx, stream, events, errs)

	return in, teeChatCapture(ctx, events, capture), errs, nil
}

// chatSendStream and chatRecvStream are the two directions of the chat client
// stream, split so each pump can only reach the one it owns.
type chatSendStream interface {
	Send(*ChatInput) error
	CloseSend() error
}

type chatRecvStream interface {
	Recv() (*ChatEvent, error)
}

// openChatCapture opens this conversation's canonical transcript capture, or
// returns nil when the conversation is not being captured (no harp) or the
// recorder could not be opened — capture is best-effort and never blocks,
// delays or alters the chat it shadows.
//
// S2: this is THE host seam behind `ctxloom run --structured` and
// `ctxloom acp` (plan §2c). The harp rides req.Env[SessionHarpEnv]: acp_cmd.go
// already stamps it there for the engine subprocess's own env, and it is
// equally available here without any ChatRequest field addition.
//
// S4 verified this env-var plumbing rather than adding to it: run.go's
// AssignSession (cmd/run.go, unconditional since well before S2/S4) mints
// activeHarp and stamps runEnv["CTXLOOM_SESSION_HARP"] regardless of
// --structured, and that env rides req.Options.Env straight into
// runStructuredREPL's agent.ChatRequest — so req.Env[SessionHarpEnv] is
// already populated on this path today. Confirmed end to end: a real
// `ctxloom run --structured` session mints a harp and writes a non-empty
// persist/transcript.jsonl.
//
// ONE recorder is opened here and shared by both producers — the
// inbound pump (records the user's own turns; the outbound tee only ever sees
// the OUTBOUND events channel, so without that tap a structured transcript
// carried assistant output but no `user` entries at all) and the outbound tee
// (records everything else). One Recorder, one seq counter, one file — a second
// NewRecorder would race the first for the file and reset Seq to 0.
//
// Those two producers used to call rec.Record (directly, or via
// transcript.RecordUserText/Tee) from their own independent goroutines — safe
// from data corruption (fileRecorder's own mutex) but with no coordination over
// record ORDER beyond raw mutex-acquisition scheduling (found via
// door-equivalence work: the same scripted conversation's transcript.jsonl
// could put the Session record before or after the first user turn across
// repeated runs of the identical script). A
// transcript.CoordinatedRecorder owns rec exclusively: both producers Submit to
// it instead of calling Record themselves, so there is exactly one caller into
// the Recorder's seq counter, not two racing it. This does not (and safely
// cannot, without risking a deadlock against a hypothetical backend that
// withholds all output until it receives input) impose a strict cross-source
// precedence — genuinely concurrent, causally-unrelated events (e.g. the
// engine's own Session event vs. the very first user turn, sent before either
// side has seen anything from the other) can still interleave either way; see
// chat_test.go's TestGRPCClient_Chat_CapturesUserTurn for why that specific
// case has no observable effect on any transcript reader.
func (c *GRPCClient) openChatCapture(ctx context.Context, req agent.ChatRequest) *transcript.CoordinatedRecorder {
	harp := req.Env[agent.SessionHarpEnv]
	if harp == "" {
		return nil
	}
	rec := c.openRecorder(ctx, harp, req.TranscriptRawPolicy)
	if rec == nil {
		return nil
	}
	return transcript.NewCoordinatedRecorder(rec, 2) // inbound tap + outbound tee
}

// pumpChatInput forwards the caller's messages onto the stream until the caller
// closes `in`, taps user turns into the capture, and half-closes the send
// direction so the backend completes.
//
// A failed Send does not release this pump: `in` is the channel the caller was
// handed and told to write to, so abandoning it mid-conversation leaves every
// later write blocked forever on a channel with no reader. It keeps draining
// until the caller closes it. Nothing further is sent, and nothing further is
// recorded either — an undelivered turn is not part of the conversation. The
// broken stream itself surfaces on `errs`, whose pump fails on the same stream.
func pumpChatInput(ctx context.Context, stream chatSendStream, in <-chan agent.ChatMessage, capture *transcript.CoordinatedRecorder) {
	if capture != nil {
		defer capture.ProducerDone()
	}
	sendFailed := false
	for msg := range in {
		if sendFailed {
			continue
		}
		if capture != nil && msg.Permission == nil && msg.Terminal == nil && !msg.CancelTurn {
			if entry := userTapEntry(msg); entry != nil {
				capture.Submit(ctx, agent.ChatEvent{Entry: entry})
			}
		}
		if serr := stream.Send(chatMessageToInput(msg)); serr != nil {
			sendFailed = true
			clidiag.Warn("ctxloom", "chat: message not delivered to the engine: %v", serr)
		}
	}
	_ = stream.CloseSend()
}

// pumpChatEvents publishes the backend's normalized events on `events` and any
// fatal receive error on `errs`, closing both when the stream ends.
func pumpChatEvents(ctx context.Context, stream chatRecvStream, events chan<- agent.ChatEvent, errs chan<- error) {
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
}

// teeChatCapture returns the event channel the caller sees. Without a capture
// that is `events` itself; with one, every event is submitted to the capture on
// its way through, and the returned channel closes only once the capture is
// durably complete — the completion barrier Chat's own doc promises.
//
// See TestGRPCClient_Chat_ConcurrentTurnsRecordAllWithoutGapsOrDuplicateSeq:
// this tee is only ONE of the capture's two registered producers — the inbound
// pump (user turns) is the other, and its ProducerDone is keyed to when the
// CALLER closes `in`, not to anything observable here. Closing the returned
// channel as soon as this producer's own loop ended let a caller treat the
// visible channel closing as "capture complete" and go read the transcript file
// — even though the inbound tap could still be mid-Submit on its final user
// turn. That is exactly the race
// TestGRPCClient_Chat_ConcurrentTurnsRecordAllWithoutGapsOrDuplicateSeq catches
// under load (79/80 records: the last user turn dropped). Blocking on
// capture.Done() — which fires only once BOTH producers have called
// ProducerDone AND every submitted record has actually been Record()'d — makes
// the close a true completion barrier. ctx.Done() is still an escape hatch so
// an abandoned/canceled call cannot hang here forever.
func teeChatCapture(ctx context.Context, events <-chan agent.ChatEvent, capture *transcript.CoordinatedRecorder) <-chan agent.ChatEvent {
	if capture == nil {
		return events
	}
	teed := make(chan agent.ChatEvent)
	go func() {
		defer close(teed)
		for ev := range events {
			capture.Submit(ctx, ev)
			teed <- ev
		}
		capture.ProducerDone()
		select {
		case <-capture.Done():
		case <-ctx.Done():
		}
	}()
	return teed
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

// chatMessageFromInput maps one ChatInput frame back to its host-side chat
// message — the exact inverse of chatMessageToInput, and named (rather than
// inlined in the server's receive loop) so the pair can be asserted for TOTAL
// field parity, not just for the fields a test happened to name (arch_test.go).
// ok is false for a frame that carries no chat message at all (a stray start, or
// a variant this build does not know), which the server skips.
func chatMessageFromInput(in *ChatInput) (agent.ChatMessage, bool) {
	switch input := in.GetInput().(type) {
	case *ChatInput_UserMessage:
		return agent.ChatMessage{
			Text:          input.UserMessage.GetText(),
			ContentBlocks: contentBlocksFromProto(input.UserMessage.GetContentBlocks()),
		}, true
	case *ChatInput_PermissionAnswer:
		return agent.ChatMessage{Permission: &agent.PermissionAnswer{
			ID:       input.PermissionAnswer.GetId(),
			OptionID: input.PermissionAnswer.GetOptionId(),
		}}, true
	case *ChatInput_CancelTurn:
		return agent.ChatMessage{CancelTurn: true}, true
	case *ChatInput_TerminalAnswer:
		return agent.ChatMessage{Terminal: terminalAnswerFromProto(input.TerminalAnswer)}, true
	default:
		return agent.ChatMessage{}, false
	}
}

// Chat is promoted from LLMRunner's embedded *GRPCClient — no
// forwarder needed.

// userTapEntry renders one outbound user message as the transcript entry the
// inbound tap records, or nil when the message carries no content at all.
//
// The tap used to require msg.Text != "", so a ContentBlocks-only turn was
// delivered to the engine and recorded NOWHERE — the transcript then showed an
// assistant reply to a prompt that never appears. Blocks are
// flattened for the Content string AND carried structurally on the entry, so
// nothing about the turn is invented and nothing is lost.
func userTapEntry(msg agent.ChatMessage) *agent.SessionEntry {
	text := msg.Text
	// Blocks ride the entry only when they are the SOLE carrier of the turn:
	// a message that already has Text records exactly as it did before, which
	// keeps the two chat doors' transcripts byte-identical (see
	// cli.TestH1bEquivalence_CanonicalTranscript).
	blocks := msg.ContentBlocks
	if text != "" {
		blocks = nil
	}
	if text == "" && len(msg.ContentBlocks) > 0 {
		parts := make([]string, 0, len(msg.ContentBlocks))
		for _, b := range msg.ContentBlocks {
			switch {
			case b.Text != "":
				parts = append(parts, b.Text)
			case b.Kind != "":
				// No text of its own (image/audio/resource): name the kind
				// rather than contributing nothing, so the turn cannot read as
				// "the user said nothing".
				parts = append(parts, "["+b.Kind+" content]")
			}
		}
		text = strings.Join(parts, "\n")
	}
	if text == "" {
		return nil
	}
	return &agent.SessionEntry{
		Type:          agent.EntryTypeUser,
		Content:       text,
		ContentBlocks: blocks,
	}
}

// Package scriptedchat is the scripted engine double the coordinator's and
// the runner's suites drive their in-process runner half with: a
// StructuredChat whose turns are scripted, gated, and recorded.
package scriptedchat

import (
	"context"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// Chat is a StructuredChat whose turns are scripted: each received
// text yields a Session event (first turn only), a thinking entry, an
// assistant echo, a tool_use/tool_result pair, and a Complete.
type Chat struct {
	Mu       sync.Mutex
	Requests []agent.ChatRequest
	Texts    []string
	TurnGate chan struct{} // non-nil: turns block until released

	// permission (C2), when set, is forwarded as ChatEvent.Permission
	// before the turn's normal entries — the turn parks until the matching
	// ChatMessage.Permission answer arrives, and answers records it.
	Permission *agent.PermissionRequest
	AnswersMu  sync.Mutex
	Answers    []agent.PermissionAnswer
	// resumable scripts the session event's ChatSessionInfo.Resumable — the
	// live loadSession capability a real ACP engine advertises (Slice 4 piece
	// 1). A one-shot child tears down at the turn boundary only when this is
	// true (live-confirmed), so a one-shot test must set it.
	Resumable bool
	// endAfterTurns, when > 0, ends the chat after that many turns — an
	// engine that exits on its own. The runner then reports RunExited.
	EndAfterTurns int
	// gotEnv / gotRunnerEnv are what the fake spawner's StartEngine was
	// handed for this engine: the ENGINE's ambient env and the RUNNER's
	// per-spawn env (the coordinator reach-back trio rides the latter only).
	GotEnv       map[string]string
	GotRunnerEnv map[string]string
}

// env is the engine's ambient environment as StartEngine received it.
// Env is the engine env the launch carried.
func (s *Chat) Env() map[string]string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.GotEnv
}

// runnerEnv is the runner's per-spawn environment as StartEngine received it.
// RunnerEnv is the runner env the launch was stamped with.
func (s *Chat) RunnerEnv() map[string]string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.GotRunnerEnv
}

// RecordedTexts is every turn text received so far.
func (s *Chat) RecordedTexts() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return append([]string(nil), s.Texts...)
}

func (s *Chat) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	defer close(out)
	s.Mu.Lock()
	s.Requests = append(s.Requests, req)
	gate := s.TurnGate
	s.Mu.Unlock()
	send := func(ev agent.ChatEvent) bool {
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{Model: req.Model, SessionID: "native-sess-42", Resumable: s.Resumable}}) {
		return ctx.Err()
	}
	turns := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-in:
			if !ok {
				return nil
			}
			if msg.Permission != nil {
				continue // a stray answer with no pending request in this fixture
			}
			if msg.Text == "" {
				continue
			}
			s.Mu.Lock()
			s.Texts = append(s.Texts, msg.Text)
			pr := s.Permission
			s.Permission = nil // forward at most once, on the first matching turn
			s.Mu.Unlock()
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if pr != nil {
				if !send(agent.ChatEvent{Permission: pr}) {
					return ctx.Err()
				}
				if !s.awaitPermissionAnswer(ctx, in, pr.ID) {
					return ctx.Err()
				}
			}
			if !send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "echo: " + msg.Text}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "grep", ToolInput: []byte(`{"q":"x"}`)}}) ||
				!send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "found"}}) ||
				!send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", InputTokens: 10, CostUSD: 0.0000015}}) {
				return ctx.Err()
			}
			turns++
			if s.EndAfterTurns > 0 && turns >= s.EndAfterTurns {
				return nil
			}
		}
	}
}

// awaitPermissionAnswer parks until a ChatMessage.Permission answering id
// arrives on in, recording it (recordedAnswers). Returns false on ctx death
// or in closing (the caller treats either as a fatal stream end, matching
// the rest of this fixture's send() convention).
func (s *Chat) awaitPermissionAnswer(ctx context.Context, in <-chan agent.ChatMessage, id string) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case msg, ok := <-in:
			if !ok {
				return false
			}
			if msg.Permission != nil && msg.Permission.ID == id {
				s.AnswersMu.Lock()
				s.Answers = append(s.Answers, *msg.Permission)
				s.AnswersMu.Unlock()
				return true
			}
		}
	}
}

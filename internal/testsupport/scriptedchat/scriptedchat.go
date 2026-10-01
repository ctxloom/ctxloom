// Package scriptedchat is the scripted engine double the coordinator's and
// the runner's suites drive their in-process runner half with: an
// engine.Instance whose structured driver's turns are scripted, gated, and
// recorded — a discrete per-turn process per Turn, as the real engines run.
package scriptedchat

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// NativeKey is the native session key every scripted turn reports: what
// the next turn resumes by.
const NativeKey = "native-sess-42"

var (
	// ErrEngineDied is the error a scripted turn fails with once
	// FailAfterTurns is reached: the engine process died mid-turn.
	ErrEngineDied = errors.New("scripted engine: the engine process died")
	// ErrEngineEnded is the error a scripted turn ends with once
	// EndAfterTurns is reached: the engine answered, then its process ended
	// the run (a non-zero exit after its output).
	ErrEngineEnded = errors.New("scripted engine: the engine process ended the run")
)

// Chat is the scripted instance. Each turn yields a Session event (the
// native key, first turn only), a thinking entry, an assistant echo, a
// tool_use/tool_result pair, and a Complete — marshalled onto engine.Event
// exactly as a real driver relays its native stream.
type Chat struct {
	Mu    sync.Mutex
	Turns []engine.Turn
	Execs []engine.Exec
	Texts []string
	Keys  []string      // the key each turn was asked to resume by
	Gate  chan struct{} // non-nil: turns block until released
	// SessionGate, when non-nil, holds the first turn's session announce
	// until released: the announce then reaches the coordinator exactly when
	// the test chooses, not the moment the process is up.
	SessionGate chan struct{}
	Answer      func(text string) string
	// FailAfterTurns, when > 0, fails the turn after that many completed —
	// an engine process that dies mid-turn. The runner then ends the run.
	FailAfterTurns int
	// EndAfterTurns, when > 0, ends the RUN after that many turns: the
	// turn's events are relayed in full and then its process ends (the
	// runner reports the terminal) — an engine that answers and exits.
	EndAfterTurns int
	// Denials, when set, are refused on every turn: each is relayed as a
	// Denied event and the turn's completion carries them all — a turn the
	// engine's posture blocked.
	Denials []agent.PermissionDenial
	// GotEnv / GotRunnerEnv are what the fake spawner's Start was handed for
	// this engine: the ENGINE's ambient env and the RUNNER's per-spawn env
	// (the coordinator reach-back trio rides the latter only).
	GotEnv       map[string]string
	GotRunnerEnv map[string]string
	turns        int
}

// Env is the engine env the launch carried.
func (s *Chat) Env() map[string]string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return s.GotEnv
}

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

// RecordedKeys is the resume key each turn was asked for, in order.
func (s *Chat) RecordedKeys() []string {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	return append([]string(nil), s.Keys...)
}

// Exec implements engine.Instance: a nominal exec over the presentations.
func (s *Chat) Exec(presented []present.Presentation) (engine.Exec, error) {
	ex := engine.Exec{Binary: "scripted", Env: map[string]string{}}
	for _, p := range presented {
		ex.Args = append(ex.Args, p.Args...)
	}
	return ex, nil
}

// Drivers implements engine.Instance: this instance is its own driver.
func (s *Chat) Drivers() []engine.StructuredDriver { return []engine.StructuredDriver{s} }

// Resume implements engine.Instance.
func (s *Chat) Resume(string) error { return nil }

// Turn implements engine.StructuredDriver.
func (s *Chat) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	plan := s.take(ex, in)
	send := func(ev agent.ChatEvent) bool {
		payload, err := json.Marshal(ev)
		if err != nil {
			panic(err)
		}
		select {
		case out <- engine.Event{Kind: ev.Kind(), Payload: payload}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// The session is announced the moment the process is up — before the
	// gate holds the turn's content, as a real engine's init precedes its
	// first answer.
	if plan.first {
		if err := awaitGate(ctx, plan.session); err != nil {
			return engine.TurnResult{}, err
		}
		if !send(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: NativeKey, Resumable: true}}) {
			return engine.TurnResult{}, ctx.Err()
		}
	}
	if err := awaitGate(ctx, plan.gate); err != nil {
		return engine.TurnResult{}, err
	}
	if plan.fail {
		return engine.TurnResult{}, ErrEngineDied
	}
	text := "echo: " + in.Prompt
	if plan.answer != nil {
		text = plan.answer(in.Prompt)
	}
	if !sendTurnBody(send, text, plan.denials) {
		return engine.TurnResult{}, ctx.Err()
	}
	if plan.end {
		return engine.TurnResult{NativeKey: NativeKey, Answer: text}, ErrEngineEnded
	}
	return engine.TurnResult{NativeKey: NativeKey, Answer: text}, nil
}

// turnPlan is what one turn does, fixed under the lock as the turn is taken.
type turnPlan struct {
	gate    <-chan struct{}
	session <-chan struct{}
	answer  func(string) string
	first   bool
	fail    bool
	end     bool
	denials []agent.PermissionDenial
}

// take records the turn and decides, under the lock, how it goes.
func (s *Chat) take(ex engine.Exec, in engine.Turn) turnPlan {
	s.Mu.Lock()
	defer s.Mu.Unlock()
	s.Turns = append(s.Turns, in)
	s.Execs = append(s.Execs, ex)
	s.Texts = append(s.Texts, in.Prompt)
	s.Keys = append(s.Keys, in.Resume)
	plan := turnPlan{gate: s.Gate, session: s.SessionGate, answer: s.Answer, first: s.turns == 0, denials: s.Denials}
	s.turns++
	plan.fail = s.FailAfterTurns > 0 && s.turns > s.FailAfterTurns
	plan.end = s.EndAfterTurns > 0 && s.turns >= s.EndAfterTurns
	return plan
}

// awaitGate holds the turn until gate opens (a nil gate is open), or ctx
// ends.
func awaitGate(ctx context.Context, gate <-chan struct{}) error {
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// sendTurnBody sends the turn's fixed event sequence around text, false as
// soon as a send is abandoned.
func sendTurnBody(send func(agent.ChatEvent) bool, text string, denials []agent.PermissionDenial) bool {
	evs := []agent.ChatEvent{
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "pondering"}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: text}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "grep", ToolInput: []byte(`{"q":"x"}`)}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolOutput: "found"}},
	}
	for i := range denials {
		evs = append(evs, agent.ChatEvent{Denied: &denials[i]})
	}
	evs = append(evs, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn", InputTokens: 10, CostUSD: 0.0000015, Denials: denials}})
	for _, ev := range evs {
		if !send(ev) {
			return false
		}
	}
	return true
}

var _ engine.Instance = (*Chat)(nil)

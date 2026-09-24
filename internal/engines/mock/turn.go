package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The mock's STRUCTURED turn: a deterministic echo, one discrete "process"
// per turn, relayed as the chat events a real driver relays. The default
// turn is an ECHO — one assistant entry ("mock chat: <text>") plus a
// completion, the echoed text proving exactly what was delivered to the
// engine (context lead blocks included). Markers script the vocabulary:
//
//   - "TOOLS": the turn emits the FULL entry vocabulary a real engine
//     produces — thinking, tool_use, tool_result, assistant — before
//     completing. A liveness check asserts entry-type VARIETY, which is only
//     meaningful against a stub that can produce more than one type.
//   - "HANG": a STALLED engine — the turn is taken (hooks fire, the record is
//     written) and then emits nothing at all until its context ends. A
//     liveness check's red direction needs an engine that goes silent.
//
// The knobs (EnvResponse and kin) script the reply and the evidence; the
// hooks the delivered hook file registers fire for every event the turn
// passes through (hooks.go).

// Turn implements engine.StructuredDriver.
func (d driver) Turn(ctx context.Context, ex engine.Exec, in engine.Turn, out chan<- engine.Event) (engine.TurnResult, error) {
	hooks, err := deliveredHooks(ex)
	if err != nil {
		return engine.TurnResult{}, err
	}
	tool, hasTool := toolCallIn(in.Prompt)
	for _, event := range eventsOfTurn(tool, hasTool) {
		if !d.fires[event] {
			continue
		}
		if err := fireHooks(ctx, ex, hooks, event, tool); err != nil {
			return engine.TurnResult{}, err
		}
	}
	if err := recordTurn(ex, in.Prompt); err != nil {
		return engine.TurnResult{}, err
	}
	if code := Env(ex.Env, EnvExitCode); code != "" {
		if n, perr := strconv.Atoi(code); perr == nil && n != 0 {
			return engine.TurnResult{}, fmt.Errorf("mock: the engine process exited %d", n)
		}
	}
	if strings.Contains(in.Prompt, "HANG") {
		// Parks BEFORE the first send, so the stall leaves no session event,
		// entry or completion behind — the signature liveness must catch.
		<-ctx.Done()
		return engine.TurnResult{}, ctx.Err()
	}

	// A nil out relays nothing: the caller wants the result alone.
	send := func(ev agent.ChatEvent) error {
		if out == nil {
			return nil
		}
		payload, merr := json.Marshal(ev)
		if merr != nil {
			return merr
		}
		select {
		case out <- engine.Event{Kind: ev.Kind(), Payload: payload}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := send(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: sessionKey, Resumable: true}}); err != nil {
		return engine.TurnResult{}, err
	}
	custom, hasCustom := LookupEnv(ex.Env, EnvResponse)
	failPrefix := Env(ex.Env, EnvFailPrefix) == "1"
	var answer string
	if hasCustom {
		answer = Response(custom, true, "", "", 1, 0, failPrefix)
	} else {
		answer = "mock chat: " + in.Prompt
		if failPrefix {
			answer = FailPrefix + "\n" + answer
		}
	}
	if strings.Contains(in.Prompt, "TOOLS") {
		for _, ev := range toolsTurn(in.Prompt) {
			if err := send(ev); err != nil {
				return engine.TurnResult{}, err
			}
		}
	}
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: answer}}); err != nil {
		return engine.TurnResult{}, err
	}
	if err := send(agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}); err != nil {
		return engine.TurnResult{}, err
	}
	return engine.TurnResult{NativeKey: sessionKey, Answer: answer}, nil
}

// toolsTurn is the entry vocabulary a TOOLS turn relays before its answer.
// The tool call is entirely synthetic (nothing is executed): the point is
// the ENTRY VOCABULARY on the wire and in the canonical transcript, not
// tool behaviour. Every entry carries the turn's text so the payload still
// proves exactly what was delivered to the engine.
func toolsTurn(text string) []agent.ChatEvent {
	return []agent.ChatEvent{
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeThinking, Content: "mock thinking: " + text}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: "mock_tool", ToolCallID: "mock-tool-1", ToolInput: json.RawMessage(`{"action":"scripted"}`), Content: "mock tool_use: " + text}},
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolCallID: "mock-tool-1", ToolOutput: "mock tool_result: " + text}},
	}
}

// recordTurn writes the turn's evidence when EnvRecordFile names a file:
// the prompt as delivered (the context lead is IN it — the runner composes
// context and prompt into one lead block), and what the runner's delivery
// left on disk, read back off the FILES — the context file the exec's argv
// names, the deny list its sibling settings file carries, the skills its
// sibling skills dir holds — never a Setup of the mock's own.
func recordTurn(ex engine.Exec, prompt string) error {
	file := Env(ex.Env, EnvRecordFile)
	if file == "" {
		return nil
	}
	rec := Record{Mode: 1, WorkDir: ex.WorkDir, Env: ex.Env, Prompt: prompt}
	if contextFile := argOf(ex, contextFlag); contextFile != "" {
		if body, err := os.ReadFile(contextFile); err == nil {
			rec.Context = string(body)
		}
		root := filepath.Dir(contextFile)
		rec.DenyTools = deliveredDenyTools(filepath.Join(root, settingsRel))
		rec.Skills = deliveredSkills(filepath.Join(root, skillsRel))
	}
	return WriteRecord(file, rec)
}

// argOf reads the value following flag on the exec's argv; "" when absent.
func argOf(ex engine.Exec, flag string) string {
	for i, a := range ex.Args {
		if a == flag && i+1 < len(ex.Args) {
			return ex.Args[i+1]
		}
	}
	return ""
}

// deliveredDenyTools reads the deny list the settings surface wrote; nil
// when no settings were delivered.
func deliveredDenyTools(path string) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var settings struct {
		DenyTools []string `json:"denyTools"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil
	}
	return settings.DenyTools
}

// deliveredSkills lists the skills the skills surface delivered, sorted;
// nil when none were.
func deliveredSkills(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

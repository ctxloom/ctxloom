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
	"github.com/ctxloom/ctxloom/internal/core/wire"
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
//   - "mock:deny=<tool>": an ENGINE-POLICY DENIAL — the tool_use, the refusal
//     as it happens (ChatEvent.Denied) and the completion carrying it
//     (TurnMeta.Denials): what claude reports when its posture refuses a
//     call nobody can approve.
//   - "mock:ask=<tool>:<json>": a call the rules leave open, ASKED about —
//     through the session endpoint's permission host and the delivered
//     permission_ask hooks when the approver is the human, denied at once
//     otherwise (ask.go).
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
	if err := d.fireTurnHooks(ctx, ex, hooks, in.Prompt); err != nil {
		return engine.TurnResult{}, err
	}
	if err := recordTurn(ex, in.Prompt, in.Posture); err != nil {
		return engine.TurnResult{}, err
	}
	if err := exitCodeErr(ex); err != nil {
		return engine.TurnResult{}, err
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
	answer := mockAnswer(ex, in.Prompt)
	ask := func(tool string, input json.RawMessage) (*agent.PermissionDenial, error) {
		return d.ask(ctx, ex, hooks, send, tool, input, in.Posture.Grants)
	}
	if err := sendTurnEvents(send, ask, in.Prompt, answer, in.Posture); err != nil {
		return engine.TurnResult{}, err
	}
	return engine.TurnResult{NativeKey: sessionKey, Answer: answer}, nil
}

// fireTurnHooks fires the delivered hooks for every event the turn passes
// through that this driver fires.
func (d driver) fireTurnHooks(ctx context.Context, ex engine.Exec, hooks wire.UnifiedHooks, prompt string) error {
	tool, hasTool := toolCallIn(prompt)
	for _, event := range eventsOfTurn(tool, hasTool) {
		if !d.fires[event] {
			continue
		}
		if err := fireHooks(ctx, ex, hooks, event, tool); err != nil {
			return err
		}
	}
	return nil
}

// exitCodeErr is the failure a scripted non-zero exit code stands for.
func exitCodeErr(ex engine.Exec) error {
	code := Env(ex.Env, EnvExitCode)
	if code == "" {
		return nil
	}
	if n, perr := strconv.Atoi(code); perr == nil && n != 0 {
		return fmt.Errorf("mock: the engine process exited %d", n)
	}
	return nil
}

// mockAnswer is the turn's reply: the scripted response when one is set,
// else an echo of the prompt, prefixed by the fail marker when asked.
func mockAnswer(ex engine.Exec, prompt string) string {
	failPrefix := Env(ex.Env, EnvFailPrefix) == "1"
	if custom, ok := LookupEnv(ex.Env, EnvResponse); ok {
		return Response(custom, true, "", "", 1, 0, failPrefix)
	}
	answer := "mock chat: " + prompt
	if failPrefix {
		answer = FailPrefix + "\n" + answer
	}
	return answer
}

// sendTurnEvents relays the turn: the resumable session at the turn's
// mode, a TOOLS turn's entries, a mock:deny or mock:ask call (ask makes the
// latter), the answer, and the completion.
func sendTurnEvents(send func(agent.ChatEvent) error, ask func(string, json.RawMessage) (*agent.PermissionDenial, error), prompt, answer string, posture engine.TurnPosture) error {
	if err := send(agent.ChatEvent{Session: &agent.ChatSessionInfo{SessionID: sessionKey, Resumable: true, PermissionMode: posture.Mode}}); err != nil {
		return err
	}
	if strings.Contains(prompt, "TOOLS") {
		for _, ev := range toolsTurn(prompt) {
			if err := send(ev); err != nil {
				return err
			}
		}
	}
	meta := &agent.TurnMeta{StopReason: "end_turn"}
	denials, err := markedDenials(send, ask, prompt)
	if err != nil {
		return err
	}
	meta.Denials = denials
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: answer}}); err != nil {
		return err
	}
	return send(agent.ChatEvent{Complete: meta})
}

// markedDenials makes the turn's marked calls — a mock:deny call, refused by
// policy, and a mock:ask call, asked about (ask) — and returns the denials
// the turn reports.
func markedDenials(send func(agent.ChatEvent) error, ask func(string, json.RawMessage) (*agent.PermissionDenial, error), prompt string) ([]agent.PermissionDenial, error) {
	var out []agent.PermissionDenial
	if tool, ok := deniedToolIn(prompt); ok {
		denial, err := policyDenial(send, tool)
		if err != nil {
			return nil, err
		}
		out = append(out, denial)
	}
	tool, input, asks, err := askIn(prompt)
	if err != nil || !asks {
		return out, err
	}
	denial, err := ask(tool, input)
	if err != nil {
		return nil, err
	}
	if denial != nil {
		out = append(out, *denial)
	}
	return out, nil
}

// policyDenial is a mock:deny call: the tool_use, then the refusal as it
// happens.
func policyDenial(send func(agent.ChatEvent) error, tool string) (agent.PermissionDenial, error) {
	denial := agent.PermissionDenial{ToolName: tool, ToolCallID: "mock-deny-1", Reason: "mock: " + tool + " is denied by policy", Decider: agent.DeciderPolicy}
	if err := send(agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: tool, ToolCallID: denial.ToolCallID, ToolInput: json.RawMessage(`{}`)}}); err != nil {
		return denial, err
	}
	return denial, send(agent.ChatEvent{Denied: &denial})
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
func recordTurn(ex engine.Exec, prompt string, posture engine.TurnPosture) error {
	file := Env(ex.Env, EnvRecordFile)
	if file == "" {
		return nil
	}
	rec := Record{Mode: 1, WorkDir: ex.WorkDir, Env: ex.Env, Prompt: prompt, Posture: &posture}
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

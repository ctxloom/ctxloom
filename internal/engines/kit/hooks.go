package kit

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// HookEvents is an engine's unified⇄native hook event table: the native
// event each unified one it fires is registered under. An event carried by a
// matcher-narrowed native one (pre_shell on a pre-tool event) is not listed:
// it decodes as the broad event the engine fired.
type HookEvents []struct{ Unified, Native string }

// Unified maps a native or unified event name to the unified one; a name the
// table does not list is carried verbatim.
func (t HookEvents) Unified(name string) string {
	for _, e := range t {
		if e.Native == name || e.Unified == name {
			return e.Unified
		}
	}
	return name
}

// Map is the table as unified→native lookups, for an engine's hook writer
// routing each unified event to the native point it fires on.
func (t HookEvents) Map() map[string]string {
	out := make(map[string]string, len(t))
	for _, e := range t {
		out[e.Unified] = e.Native
	}
	return out
}

// NativeHookPayload is ctxloom's OWN hook wire: what an engine whose hooks
// ctxloom fires itself (the engine's runtime, or a bridge ctxloom generated
// into it) writes to a hook's stdin. Every field but the event and the
// session is per event; the tool's input and response stay raw, their shape
// being the tool's.
type NativeHookPayload struct {
	Event        string          `json:"event"`
	Session      string          `json:"session"`
	Transcript   string          `json:"transcript,omitempty"`
	Source       string          `json:"source,omitempty"`
	Prompt       string          `json:"prompt,omitempty"`
	Tool         string          `json:"tool,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`
	Cwd          string          `json:"cwd,omitempty"`
}

// NativeHookAnswer is ctxloom's own answer wire: a hook's stdout, read back by
// whatever fired it.
type NativeHookAnswer struct {
	Context string `json:"context,omitempty"`
	Notice  string `json:"notice,omitempty"`
	Block   bool   `json:"block,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// NativeHookCodec is engine.HookCodec over ctxloom's own wire
// (NativeHookPayload, NativeHookAnswer). The engine supplies only what its
// tools and its firing side decide: its event table, how its skill tool and
// its file-editing tools name what they touched, and which events can carry
// an answer's context or block.
type NativeHookCodec struct {
	// Engine names the engine in an error's wording only.
	Engine engine.Name
	// Events maps a payload's event to the unified one; nil = the payload
	// names the unified event itself.
	Events HookEvents
	// Skill is engine.HookCodec.InvokedSkill; nil = no tool runs a skill.
	Skill func(tool string, input []byte) (string, bool)
	// EditedPath is the file a tool call edited, "" for none; nil = none.
	EditedPath func(tool string, input []byte) string
	// ContextOn and BlockOn are the unified events an answer may carry
	// context on and block; nil = every event.
	ContextOn, BlockOn []string
	// Limit is engine.HookCodec.ContextLimit.
	Limit int
}

// Decode reads one payload. The event is the payload's own, else the one the
// hook was registered under, mapped to the unified name.
func (c NativeHookCodec) Decode(event string, payload []byte) (engine.HookEvent, error) {
	var p NativeHookPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return engine.HookEvent{}, fmt.Errorf("%s hook payload: %w", c.Engine, err)
	}
	if p.Event == "" {
		p.Event = event
	}
	out := engine.HookEvent{
		Event:         c.Events.Unified(p.Event),
		NativeSession: p.Session,
		Transcript:    p.Transcript,
		Source:        p.Source,
		Prompt:        p.Prompt,
		Tool:          p.Tool,
		ToolInput:     p.ToolInput,
		ToolResponse:  p.ToolResponse,
	}
	out.Skill, _ = c.InvokedSkill(p.Tool, p.ToolInput)
	if c.EditedPath != nil {
		out.Path = c.EditedPath(p.Tool, p.ToolInput)
	}
	return out, nil
}

// Encode renders r as the answer to a hook registered under event: one JSON
// line, exit 0. Context or a block on an event the engine cannot carry it on
// is refused.
func (c NativeHookCodec) Encode(event string, r engine.HookResponse) (engine.HookReply, error) {
	if r.Context != "" && c.ContextOn != nil && !slices.Contains(c.ContextOn, event) {
		return engine.HookReply{}, fmt.Errorf("%s carries no hook context on %s", c.Engine, event)
	}
	if r.Block && c.BlockOn != nil && !slices.Contains(c.BlockOn, event) {
		return engine.HookReply{}, fmt.Errorf("%s cannot block %s from a hook", c.Engine, event)
	}
	b, err := json.Marshal(NativeHookAnswer(r))
	if err != nil {
		return engine.HookReply{}, err
	}
	return engine.HookReply{Stdout: append(b, '\n')}, nil
}

// ContextLimit is the engine's per-answer context bound (0 = none).
func (c NativeHookCodec) ContextLimit() int { return c.Limit }

// InvokedSkill is the skill a tool call ran, by the engine's Skill.
func (c NativeHookCodec) InvokedSkill(tool string, input []byte) (string, bool) {
	if c.Skill == nil {
		return "", false
	}
	return c.Skill(tool, input)
}

var _ engine.HookCodec = NativeHookCodec{}

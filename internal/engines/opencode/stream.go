package opencode

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// This file is opencode's line codec for `opencode run --format json`
// (v1.18.x): every stdout line is one event,
//
//	{"type", "timestamp", "sessionID", "part" | "error"}
//
// where type is step_start, step_finish (once per model step, carrying its
// finish reason, cost and tokens), text and reasoning (each once its part
// has ended; no deltas), tool_use (a tool call that completed or failed) or
// error (a session error: {name, data}). A child session's parts are never
// emitted. The process exits 1 after any error event, and is killed without
// last words on an interrupt.

// ocLine is one event line.
type ocLine struct {
	Type      string   `json:"type"`
	SessionID string   `json:"sessionID"`
	Part      *ocPart  `json:"part"`
	Error     *ocError `json:"error"`
}

type ocPart struct {
	Type    string       `json:"type"`
	Text    string       `json:"text"`
	Ignored bool         `json:"ignored"`
	Time    *ocTime      `json:"time"`
	CallID  string       `json:"callID"`
	Tool    string       `json:"tool"`
	State   *ocToolState `json:"state"`
	Reason  string       `json:"reason"`
	Cost    float64      `json:"cost"`
	Tokens  *ocTokens    `json:"tokens"`
}

type ocTime struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type ocToolState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output string          `json:"output"`
	Error  string          `json:"error"`
	Time   *ocTime         `json:"time"`
}

type ocTokens struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cache  struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}

// ocError is a session error as opencode serializes a named error.
type ocError struct {
	Name string      `json:"name"`
	Data ocErrorData `json:"data"`
}

// ocErrorData is the fields of a named error's data the failure table reads
// (APIError's; the other errors carry a message alone).
type ocErrorData struct {
	Message         string            `json:"message"`
	StatusCode      *int              `json:"statusCode"`
	ResponseHeaders map[string]string `json:"responseHeaders"`
	Metadata        map[string]string `json:"metadata"`
}

// The step finish reason of a step that called tools: the turn goes on.
const reasonToolCalls = "tool-calls"

// The stop reasons of a turn opencode ended with an error and no final
// step: an unrecovered context overflow, or anything else.
const (
	stopContextOverflow = "context_overflow"
	stopError           = "error"
)

// sessionTotals is what a session's completions report as running totals
// (agent.TurnMeta): each turn is its own opencode process, which knows only
// its own steps, so the instance carries the totals across turns.
type sessionTotals struct {
	output int
	cost   float64
}

// turnStream maps one turn's lines (kit.LineMapper). It announces the
// session once, maps text, reasoning and tool calls to entries, folds every
// step's usage, and completes the turn at its final step (one that did not
// stop to call tools). End completes a turn the stream ended early, so its
// exit is never read as a death:
//   - one that saw an error after its last completion, with the error's
//     stop reason (opencode may recover from a context overflow by
//     compacting, so an overflow decides nothing until the stream ends);
//   - one a permission refusal halted: `run` rejecting an ask ends the turn
//     right after the step that called the tool, with no final step.
//
// A turn that merely stops after a tool step, with neither, is left
// incomplete: the process died mid-turn.
type turnStream struct {
	window  int
	totals  *sessionTotals
	now     func() time.Time
	session bool
	pending string // the stop reason an error left, until a completion clears it
	open    string // the reason of a step since the last completion
	denied  bool   // a call was refused since the last completion
	// denials are the turn's refused calls, listed on its completions (the
	// driver joins each Denied's reason onto them).
	denials []agent.PermissionDenial
	last    ocTokens
}

func newTurnStream(window int, totals *sessionTotals, now func() time.Time) kit.LineMapper {
	if now == nil {
		now = time.Now
	}
	return &turnStream{window: window, totals: totals, now: now}
}

func (m *turnStream) Map(raw []byte) []agent.ChatEvent {
	var l ocLine
	if json.Unmarshal(raw, &l) != nil {
		return nil
	}
	return append(m.announce(l.SessionID), m.event(l)...)
}

// announce is the session, once: the key the next turn resumes by.
func (m *turnStream) announce(id string) []agent.ChatEvent {
	if m.session || id == "" {
		return nil
	}
	m.session = true
	return []agent.ChatEvent{{Session: &agent.ChatSessionInfo{SessionID: id}}}
}

// event maps one line's payload.
func (m *turnStream) event(l ocLine) []agent.ChatEvent {
	if l.Type == "error" && l.Error != nil {
		return m.failed(l.Error)
	}
	if l.Part == nil {
		return nil
	}
	switch l.Type {
	case "text", "reasoning":
		return partEvents(l.Part)
	case "tool_use":
		return m.tool(l.Part)
	case "step_finish":
		return m.step(l.Part)
	}
	return nil
}

// partEvents maps one finished part of an assistant message, as the live
// stream and the bridge transcript (MessageEvents) both carry it: text and
// reasoning as entries, a tool call as its use and result.
func partEvents(p *ocPart) []agent.ChatEvent {
	switch p.Type {
	case "text":
		return []agent.ChatEvent{entry(agent.EntryTypeAssistant, p)}
	case "reasoning":
		return []agent.ChatEvent{entry(agent.EntryTypeThinking, p)}
	case "tool":
		return toolEvents(p)
	}
	return nil
}

// ocMessage is one message as opencode's API holds it, a line of the bridge
// transcript.
type ocMessage struct {
	Info struct {
		Role string `json:"role"`
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
	} `json:"info"`
	Parts []ocPart `json:"parts"`
}

// MessageEvents maps one message as opencode's API holds it ({info, parts},
// a bridge transcript line) to entries: a user message's text, an assistant
// message's parts (partEvents). A line that is not a message is an error.
func MessageEvents(raw []byte) ([]agent.ChatEvent, error) {
	var m ocMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m.Info.Role == "" {
		return nil, errNotAMessage
	}
	var out []agent.ChatEvent
	for i := range m.Parts {
		p := &m.Parts[i]
		if m.Info.Role != "user" {
			out = append(out, partEvents(p)...)
			continue
		}
		if p.Type == "text" && !p.Ignored && p.Text != "" {
			out = append(out, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: p.Text, Timestamp: time.UnixMilli(m.Info.Time.Created)}})
		}
	}
	return out, nil
}

var errNotAMessage = errors.New("not an opencode message: no info.role")

// tool is a finished tool call, each refusal in it kept for the turn's
// completions.
func (m *turnStream) tool(p *ocPart) []agent.ChatEvent {
	evs := toolEvents(p)
	for _, e := range evs {
		if d := e.Denied; d != nil {
			m.denied = true
			m.denials = append(m.denials, agent.PermissionDenial{ToolName: d.ToolName, ToolCallID: d.ToolCallID, Decider: d.Decider})
		}
	}
	return evs
}

// End completes a turn an error or a refusal ended with no final step.
func (m *turnStream) End() []agent.ChatEvent {
	switch {
	case m.pending != "":
		return []agent.ChatEvent{{Complete: m.meta(m.pending)}}
	case m.open != "" && m.denied:
		return []agent.ChatEvent{{Complete: m.meta(m.open)}}
	}
	return nil
}

// entry is a text or reasoning part as an entry, at the time it ended.
func entry(t agent.SessionEntryType, p *ocPart) agent.ChatEvent {
	e := &agent.SessionEntry{Type: t, Content: p.Text}
	if p.Time != nil && p.Time.End > 0 {
		e.Timestamp = time.UnixMilli(p.Time.End)
	}
	return agent.ChatEvent{Entry: e}
}

// toolEvents is a finished tool call: its use and its result (the error as
// the result of a failed one), and a Denied when what failed it is
// opencode's permission refusal.
func toolEvents(p *ocPart) []agent.ChatEvent {
	s := p.State
	if s == nil || (s.Status != "completed" && s.Status != "error") {
		return nil
	}
	var at time.Time
	if s.Time != nil && s.Time.End > 0 {
		at = time.UnixMilli(s.Time.End)
	}
	use := &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: p.Tool, ToolCallID: p.CallID, ToolInput: s.Input, Timestamp: at}
	result := &agent.SessionEntry{Type: agent.EntryTypeToolResult, ToolName: p.Tool, ToolCallID: p.CallID, ToolOutput: s.Output, Timestamp: at}
	out := []agent.ChatEvent{{Entry: use}, {Entry: result}}
	if s.Status == "error" {
		result.ToolOutput, result.IsError = s.Error, true
		if permissionRefused(s.Error) {
			out = append(out, agent.ChatEvent{Denied: &agent.PermissionDenial{ToolName: p.Tool, ToolCallID: p.CallID, Reason: s.Error, Decider: agent.DeciderPolicy}})
		}
	}
	return out
}

// permissionRefused recognises opencode's permission errors as a tool call's
// error: a rule that denies the call, or an ask `run` rejected.
func permissionRefused(msg string) bool {
	return strings.Contains(msg, "The user has specified a rule which prevents you from using this specific tool call") ||
		strings.Contains(msg, "The user rejected permission to use this specific tool call")
}

// step folds one step's usage; a final step completes the turn.
func (m *turnStream) step(p *ocPart) []agent.ChatEvent {
	if p.Tokens != nil {
		m.last = *p.Tokens
		m.totals.output += int(p.Tokens.Output)
	}
	m.totals.cost += p.Cost
	if p.Reason == reasonToolCalls {
		m.open = p.Reason
		return nil
	}
	m.pending, m.open, m.denied = "", "", false
	return []agent.ChatEvent{{Complete: m.meta(p.Reason)}}
}

// meta is a completion's accounting: what is in context as of the last step,
// the session's output and cost so far, the label's window. Its denials are
// the turn's, which the driver joins on.
func (m *turnStream) meta(stop string) *agent.TurnMeta {
	return &agent.TurnMeta{
		InputTokens:         int(m.last.Input),
		CacheReadTokens:     int(m.last.Cache.Read),
		CacheCreationTokens: int(m.last.Cache.Write),
		OutputTokens:        m.totals.output,
		CostUSD:             m.totals.cost,
		ContextWindow:       m.window,
		StopReason:          stop,
		Denials:             slices.Clone(m.denials),
	}
}

// failed classifies a session error: a kind the coordinator holds the run on
// is reported now; every error leaves its stop reason for End.
func (m *turnStream) failed(e *ocError) []agent.ChatEvent {
	if e.Name == "ContextOverflowError" {
		m.pending = stopContextOverflow
		return nil
	}
	m.pending = stopError
	f := failure(e, m.now)
	if f == nil {
		return nil
	}
	return []agent.ChatEvent{{Failed: f}}
}

// unreachable matches the messages of a request that never reached the
// endpoint (opencode's own retry patterns for a connection failure).
var unreachable = regexp.MustCompile(`(?i)econnrefused|econnreset|enotfound|eai_again|etimedout|getaddrinfo|fetch failed|failed to fetch|unable to connect|cannot connect|connection refused|connection reset|connection error|network[-_\s]error|socket hang up`)

// failure is opencode's failure table: the shared kind a session error is,
// or nil for one that holds nothing (the turn simply ended in error).
// opencode has already retried a retryable error up to five times.
func failure(e *ocError, now func() time.Time) *agent.TurnFailure {
	d := e.Data
	switch {
	case e.Name == "ProviderAuthError":
		return &agent.TurnFailure{Kind: agent.FailureCredentialRejected}
	case e.Name != "APIError" && e.Name != "UnknownError":
		return nil
	case strings.HasSuffix(d.Metadata["code"], "HeaderTimeoutError"):
		// A single-slot server queues the request; it looks exactly like this.
		return &agent.TurnFailure{Kind: agent.FailureOverloaded}
	case d.StatusCode == nil:
		if unreachable.MatchString(d.Message) || unreachable.MatchString(d.Metadata["code"]) {
			return &agent.TurnFailure{Kind: agent.FailureEndpointUnreachable}
		}
		return nil
	}
	return statusFailure(*d.StatusCode, d.ResponseHeaders, now)
}

// statusFailure is the kind an HTTP status from the endpoint is, or nil.
func statusFailure(code int, headers map[string]string, now func() time.Time) *agent.TurnFailure {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &agent.TurnFailure{Kind: agent.FailureCredentialRejected}
	case http.StatusTooManyRequests:
		return &agent.TurnFailure{Kind: agent.FailureRateLimited, ResetsAt: retryAfter(headers, now)}
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return &agent.TurnFailure{Kind: agent.FailureOverloaded}
	}
	return nil
}

// retryAfter is when a 429's Retry-After says the limit lifts (seconds or an
// HTTP date); zero when it says nothing usable.
func retryAfter(h map[string]string, now func() time.Time) time.Time {
	v := strings.TrimSpace(h["retry-after"])
	if v == "" {
		return time.Time{}
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return now().Add(time.Duration(s) * time.Second)
	}
	if t, err := http.ParseTime(v); err == nil {
		return t
	}
	return time.Time{}
}

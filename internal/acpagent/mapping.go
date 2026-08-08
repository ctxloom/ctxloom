package acpagent

import (
	"encoding/json"
	"strconv"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file is the INVERSE of internal/acp/mapping.go: it maps ctxloom's
// backend-agnostic chat entries onto ACP `session/update` notifications for
// the outer client.
//
//	EntryTypeAssistant  → agent_message_chunk
//	EntryTypeThinking   → agent_thought_chunk
//	EntryTypeToolUse    → tool_call        (e.ToolCallID reused verbatim when the
//	                                        producing backend supplied one — e.g.
//	                                        internal/acp's ACP-native client
//	                                        mapping — else a fresh id generated
//	                                        and pushed per tool name exactly as
//	                                        before; e.ToolKind/e.ToolLocations
//	                                        carry through — IR2)
//	EntryTypeToolResult → tool_call_update (e.ToolCallID, when present, targets
//	                                        the SAME id directly instead of
//	                                        popping a FIFO guess by tool name —
//	                                        this is the fix for the confirmed
//	                                        call-1/call-2 mispair risk: two
//	                                        concurrent calls to the same tool
//	                                        name used to risk pairing a result
//	                                        with the WRONG call. e.ToolContent,
//	                                        when present, rebuilds the
//	                                        structured content collection
//	                                        instead of only RawOutput text)
//	EntryTypeUser       → (dropped — never echo the user's message back)
//	EntryTypeSystem     → SystemKind discriminates the two producers (see
//	                      agent.SessionSystemKind):
//	                        SystemKindPlan   → a REAL ACP `plan` update, built
//	                                           from e.Plan's structured entries
//	                                           (the IR2 fix for the conformance
//	                                           audit's headline finding — a
//	                                           prior revision could only
//	                                           forward the flattened text as an
//	                                           agent_message_chunk, because the
//	                                           IR had nowhere to keep the
//	                                           structured entries; it does now)
//	                        SystemKindNotice → agent_message_chunk (unchanged:
//	                                           this is the delegated-turn-
//	                                           failure notice path,
//	                                           internal/operations/delegate.go,
//	                                           which never carried structure to
//	                                           begin with and isn't touched by
//	                                           this slice)
//	ChatSessionInfo     → split three ways (G13 decompose — see
//	                      sessionInfoUpdateWire and ctxloomSessionInfoUpdate's
//	                      doc comment for the full evidence):
//	                        Model         → already carried by CO1's
//	                                        SessionConfigOption ("model"
//	                                        category, session/new|load's
//	                                        configOptions) — NOT re-emitted
//	                                        here at all.
//	                        ContextWindow → already carried by usage_update's
//	                                        `size` (see usageUpdateWire) — NOT
//	                                        re-emitted here at all.
//	                        PermissionMode/MCPServers → session_info_update's
//	                                        `_meta` (genuinely no spec home;
//	                                        IR4-relocated off ctxloom's own
//	                                        bespoke variant name onto the
//	                                        spec's real session_info_update
//	                                        frame's `_meta` extension point)
//	TurnMeta (Complete) → usage_update (context gauge + cost; see usageUpdateWire) then ends the turn
//
// IR3: when ev.Entry is nil but ev.Raw is set (a passthrough-only event, e.g.
// available_commands_update/current_mode_update forwarded verbatim by
// internal/acp/mapping.go), mapEvent re-decodes it straight into an
// api.SessionUpdate and re-emits it — see rawOnlyUpdates. When ev.Entry IS
// set and ev.Raw ALSO carries a `_meta` supplement, that meta is folded onto
// the outbound update's own Meta field — see metaFromRaw.

// mapEvent converts one chat event into 0..1 session updates, tracking
// tool-call id pairing on the session.
func (sess *session) mapEvent(ev agent.ChatEvent) []api.SessionUpdate {
	if ev.Entry == nil {
		return rawOnlyUpdates(ev.Raw)
	}
	e := ev.Entry
	meta := metaFromRaw(ev.Raw)
	switch e.Type {
	case agent.EntryTypeAssistant:
		u := &api.SessionUpdateAgentMessageChunk{Content: textBlock(e.Content), Meta: meta}
		return []api.SessionUpdate{{AgentMessageChunk: u}}
	case agent.EntryTypeThinking:
		u := &api.SessionUpdateAgentThoughtChunk{Content: textBlock(e.Content), Meta: meta}
		return []api.SessionUpdate{{AgentThoughtChunk: u}}
	case agent.EntryTypeToolUse:
		id := sess.pushToolCall(e.ToolName, api.ToolCallId(e.ToolCallID))
		return []api.SessionUpdate{{
			ToolCall: &api.SessionUpdateToolCall{
				ToolCallId: id,
				Title:      e.ToolName,
				Status:     api.ToolCallStatusInProgress,
				RawInput:   rawValue(e.ToolInput),
				Kind:       api.ToolKind(e.ToolKind),
				Locations:  locationsFromIR(e.ToolLocations),
				Meta:       meta,
			},
		}}
	case agent.EntryTypeToolResult:
		id := sess.popToolCall(e.ToolName, api.ToolCallId(e.ToolCallID))
		status := api.ToolCallStatusCompleted
		if e.IsError {
			status = api.ToolCallStatusFailed
		}
		u := &api.SessionToolCallUpdate{
			ToolCallId: id,
			Status:     &status,
			RawOutput:  e.ToolOutput,
			Locations:  locationsFromIR(e.ToolLocations),
			Meta:       meta,
		}
		if e.ToolKind != "" {
			k := api.ToolKind(e.ToolKind)
			u.Kind = &k
		}
		if content := toolContentFromIR(e.ToolContent); content != nil {
			u.Content = content
		}
		return []api.SessionUpdate{{ToolCallUpdate: u}}
	case agent.EntryTypeSystem:
		if e.SystemKind == agent.SystemKindPlan && len(e.Plan) > 0 {
			return []api.SessionUpdate{{Plan: &api.SessionUpdatePlan{Entries: planEntriesFromIR(e.Plan), Meta: meta}}}
		}
		if e.Content == "" {
			return nil
		}
		return []api.SessionUpdate{{
			AgentMessageChunk: &api.SessionUpdateAgentMessageChunk{Content: textBlock(e.Content), Meta: meta},
		}}
	default:
		return nil
	}
}

// rawOnlyUpdates re-decodes an IR3 passthrough-only ChatEvent.Raw straight
// into an api.SessionUpdate (the SDK's own UnmarshalJSON dispatches on the
// embedded "sessionUpdate" discriminator, so this round-trips exactly what
// internal/acp/mapping.go's rawOnlyEvent marshaled) and re-emits it — but
// ONLY when the decoded result is one of the allowlisted passthrough
// variants. This re-checks the allowlist at the OUTBOUND boundary too
// (defense in depth: this function trusts nothing about how Raw got here) —
// a malformed or unexpected payload emits nothing rather than forwarding an
// unvetted frame. Permissions cannot appear here even in principle:
// session/request_permission is a request, not a session/update, so it has
// no corresponding SessionUpdate variant to decode into.
func rawOnlyUpdates(raw json.RawMessage) []api.SessionUpdate {
	if len(raw) == 0 {
		return nil
	}
	var u api.SessionUpdate
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil
	}
	switch {
	case u.AvailableCommandsUpdate != nil, u.CurrentModeUpdate != nil:
		return []api.SessionUpdate{u}
	default:
		return nil
	}
}

// metaFromRaw extracts the `_meta` object IR3 attached to an Entry-carrying
// ChatEvent's Raw field (internal/acp/mapping.go's metaRaw envelope:
// `{"_meta": ...}`), or nil when Raw is empty or carries no _meta. Any other
// shape (e.g. a raw-only passthrough frame reaching here by mistake) also
// decodes to nil rather than erroring — this is a best-effort supplement,
// never load-bearing for the entry it rides alongside.
func metaFromRaw(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m struct {
		Meta map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m.Meta
}

// locationsFromIR rebuilds ACP tool-call locations from the IR2 form.
func locationsFromIR(locs []agent.ToolLocation) []api.ToolCallLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]api.ToolCallLocation, 0, len(locs))
	for _, l := range locs {
		loc := api.ToolCallLocation{Path: l.Path}
		if l.Line != 0 {
			line := l.Line
			loc.Line = &line
		}
		out = append(out, loc)
	}
	return out
}

// toolContentFromIR rebuilds a tool call's structured content collection from
// the IR2 form. Returns nil (not an empty slice) when content carries
// nothing rebuildable, so the caller can tell "no structured content" from
// "structured content that happens to be empty" and fall back to RawOutput.
func toolContentFromIR(content []agent.ToolContentBlock) []api.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]api.ToolCallContent, 0, len(content))
	for _, c := range content {
		switch c.Kind {
		case "diff":
			d := &api.ToolCallContentDiff{Path: c.DiffPath, NewText: c.DiffNewText}
			if c.DiffOldText != "" {
				old := c.DiffOldText
				d.OldText = &old
			}
			out = append(out, api.ToolCallContent{Diff: d})
		case "terminal":
			out = append(out, api.ToolCallContent{Terminal: &api.ToolCallContentTerminal{TerminalId: api.TerminalId(c.TerminalID)}})
		default:
			// EVERY other kind, not just "content". The switch used to list
			// "content" explicitly and silently drop anything else, which
			// turned each purpose-named kind the transcript layer produces
			// (process_output, file_snapshot, tool_catalog, agent_result,
			// excluded — see agent.Kind*) into nothing at all on the way out.
			// A default that carries them is what keeps this mapping total as
			// the vocabulary grows.
			out = append(out, contentBlockFromIR(c))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// contentBlockFromIR rebuilds one outbound content block, preferring the
// flattened Text and falling back to the verbatim Raw.
//
// The Raw leg is load-bearing, not a nicety. A block whose Text is empty but
// whose Raw is populated is the NORMAL shape for anything non-textual — an
// image, an ACP resource, a claude tool_reference, a structured toolUseResult.
// Emitting textBlock(c.Text) for those sent the far client an EMPTY text block
// where the content should have been: present in the frame, carrying nothing,
// indistinguishable from a tool that genuinely returned no content.
//
// Two Raw shapes reach here and they are handled differently on purpose:
//   - a block that came IN over ACP has Raw = json.Marshal(api.ToolCallContent)
//     (internal/acp/mapping.go), so it decodes straight back and round-trips
//     losslessly. That is the round-trip full-gap named.
//   - a block from a vendor transcript reader has Raw in the ENGINE's own
//     shape, which is not an ACP variant and must not be forced into one. It
//     is emitted as text so the content is at least VISIBLE, rather than
//     silently becoming an empty block.
func contentBlockFromIR(c agent.ToolContentBlock) api.ToolCallContent {
	if c.Text != "" {
		return api.ToolCallContent{Content: &api.ToolCallContentContent{Content: textBlock(c.Text)}}
	}
	if len(c.Raw) > 0 {
		// Gated on the ACP "type" DISCRIMINATOR, not on whether the decode
		// merely succeeded. api.ToolCallContent has a custom UnmarshalJSON
		// that populates its Content variant for an arbitrary object, so
		// "err == nil" — and even "some variant is non-nil" — is satisfied by
		// a foreign vendor payload. Measured: a claude tool_reference decoded
		// "successfully" into a ToolCallContent whose inner ContentBlock was
		// zero-valued, which then failed to MARSHAL at all ("unexpected end
		// of JSON input") and would have put an unserializable frame on the
		// wire. Only a payload that names itself an ACP variant is treated as
		// one.
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(c.Raw, &probe); err == nil {
			switch probe.Type {
			case "content", "diff", "terminal":
				var rt api.ToolCallContent
				if err := json.Unmarshal(c.Raw, &rt); err == nil {
					return rt
				}
			}
		}
		return api.ToolCallContent{Content: &api.ToolCallContentContent{Content: textBlock(string(c.Raw))}}
	}
	return api.ToolCallContent{Content: &api.ToolCallContentContent{Content: textBlock("")}}
}

// planEntriesFromIR rebuilds ACP plan entries from the IR2 structured form.
// Priority/Status are non-optional ACP enum fields (no omitempty — an empty
// string would marshal as `""`, which no PlanEntryPriority/PlanEntryStatus
// value is), so an entry that somehow arrived without one (every producer
// today — internal/acp/mapping.go's mapPlan — always sets both, straight
// from the engine's own real plan update; this is a safety net for a future
// non-ACP producer) defaults to "medium"/"pending" rather than emitting an
// invalid frame.
func planEntriesFromIR(entries []agent.PlanEntry) []api.PlanEntry {
	out := make([]api.PlanEntry, 0, len(entries))
	for _, e := range entries {
		priority := api.PlanEntryPriority(e.Priority)
		if priority == "" {
			priority = api.PlanEntryPriorityMedium
		}
		status := api.PlanEntryStatus(e.Status)
		if status == "" {
			status = api.PlanEntryStatusPending
		}
		out = append(out, api.PlanEntry{Content: e.Content, Priority: priority, Status: status})
	}
	return out
}

// replayEntry maps one RECORDED session entry onto session/update
// notifications for session/load replay. Unlike the live mapping, user entries
// ARE emitted (the spec's replay includes the user's messages); everything else
// (including system/plan entries, per mapEvent above) replays exactly as it
// was live mapped.
func (sess *session) replayEntry(e agent.SessionEntry) []api.SessionUpdate {
	if e.Type == agent.EntryTypeUser {
		if e.Content == "" {
			return nil
		}
		return []api.SessionUpdate{{
			UserMessageChunk: &api.SessionUpdateUserMessageChunk{Content: textBlock(e.Content)},
		}}
	}
	return sess.mapEvent(agent.ChatEvent{Entry: &e})
}

// permissionRequestWire renders a forwarded engine permission request as the
// outbound session/request_permission body. The referenced toolCall targets
// p.ToolCallID directly when the producing backend supplied one (IR2 — same
// fix as tool_call/tool_call_update); otherwise it falls back to the most
// recent OPEN generated id for the same tool name — the engine announces the
// tool_call just before asking permission for it — or a fresh id when none is
// open (a lone toolCallId is still valid ACP).
//
// L0 checklist A2: Options is built from make(..., 0, ...) rather than a
// `var` (nil) slice, so a permission request with ZERO engine-supplied
// options still marshals `"options":[]`, not `"options":null` — the current
// spec's RequestPermissionRequest requires options to be an array (no null
// alternative); a nil slice through encoding/json is indistinguishable from
// one that was never populated, and used to marshal as `null`.
func (sess *session) permissionRequestWire(p *agent.PermissionRequest) api.RequestPermissionRequest {
	req := api.RequestPermissionRequest{
		SessionId: sess.id,
		ToolCall:  api.ToolCallUpdate{ToolCallId: sess.peekToolCall(p.ToolName, api.ToolCallId(p.ToolCallID))},
		Options:   make([]api.PermissionOption, 0, len(p.Options)),
	}
	if p.ToolName != "" {
		title := p.ToolName
		req.ToolCall.Title = &title
	}
	if v := rawValue(p.ToolInput); v != nil {
		req.ToolCall.RawInput = v
	}
	for _, o := range p.Options {
		req.Options = append(req.Options, api.PermissionOption{
			OptionId: api.PermissionOptionId(o.ID),
			Kind:     api.PermissionOptionKind(o.Kind),
			Name:     o.Name,
		})
	}
	return req
}

// nextToolCallID mints a fresh generated toolCallId (the pre-IR2 fallback
// scheme, unchanged): "call-N" from the session's monotonic counter. Callers
// hold sess.mu.
func (sess *session) nextToolCallID() api.ToolCallId {
	sess.toolSeq++
	return api.ToolCallId("call-" + strconv.FormatInt(sess.toolSeq, 10))
}

// peekToolCall returns want when the producing backend supplied an
// engine-native id (IR2); otherwise the NEWEST open call id for name without
// consuming it (the result will pop it later), or a fresh id when none is
// open.
func (sess *session) peekToolCall(name string, want api.ToolCallId) api.ToolCallId {
	if want != "" {
		return want
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if ids := sess.openCall[name]; len(ids) > 0 {
		return ids[len(ids)-1]
	}
	return sess.nextToolCallID()
}

// pushToolCall records the open call for name so the eventual result can
// target it, and returns the id to use: want, when the producing backend
// supplied an engine-native one (IR2 — the fix for the same-name mispair
// risk, since the matching tool_result will carry the SAME want and
// popToolCall below honors it directly instead of guessing by FIFO order);
// otherwise a freshly generated one, exactly as before this field existed.
func (sess *session) pushToolCall(name string, want api.ToolCallId) api.ToolCallId {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	id := want
	if id == "" {
		id = sess.nextToolCallID()
	}
	sess.openCall[name] = append(sess.openCall[name], id)
	return id
}

// popToolCall pairs a result with its call. When want is set (IR2: the
// producing backend supplied the SAME engine-native id on both the tool_call
// and its result), that id is honored DIRECTLY — no name-based guessing at
// all — and dropped from the open-call bookkeeping if still present there
// (so a later permission peek doesn't see a stale open id). Otherwise it
// falls back to the OLDEST open call of the same tool name (FIFO — engines
// report results in call order): the pre-IR2 behavior, kept for backends
// that carry no native id. An unmatched result with no open call of that name
// gets a fresh id: a lone tool_call_update is still valid ACP, and honesty
// beats mis-pairing.
func (sess *session) popToolCall(name string, want api.ToolCallId) api.ToolCallId {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if want != "" {
		if ids := sess.openCall[name]; len(ids) > 0 {
			for i, id := range ids {
				if id == want {
					sess.openCall[name] = append(ids[:i:i], ids[i+1:]...)
					break
				}
			}
		}
		return want
	}
	if ids := sess.openCall[name]; len(ids) > 0 {
		id := ids[0]
		sess.openCall[name] = ids[1:]
		return id
	}
	return sess.nextToolCallID()
}

// sessionInfoUpdateWire projects one ChatSessionInfo (the engine's one-time
// start-of-chat metadata) onto the wire. G13 (decomposing IR4): only the two
// facts with genuinely no spec home (PermissionMode, MCPServers) ride here,
// inside a REAL session_info_update frame's `_meta` object — see
// ctxloomSessionInfoUpdate's doc comment in wire.go for the full per-fact
// evidence. info.Model and info.ContextWindow are deliberately NOT read here
// at all: Model already rides CO1's SessionConfigOption ("model" category),
// and ContextWindow already rides usage_update's `size` (usageUpdateWire) —
// re-emitting them here would be pure duplication of a channel that already
// fires. Returns nil when there is nothing worth surfacing (so a
// ChatSessionInfo with no permission mode or MCP status emits no
// notification, even if it carries a Model/ContextWindow that rides
// elsewhere).
func sessionInfoUpdateWire(info *agent.ChatSessionInfo) any {
	if info == nil || (info.PermissionMode == "" && len(info.MCPServers) == 0) {
		return nil
	}
	u := ctxloomSessionInfoUpdate{PermissionMode: info.PermissionMode}
	for _, m := range info.MCPServers {
		u.McpServers = append(u.McpServers, mcpStatusWire{Name: m.Name, Status: m.Status})
	}
	return api.SessionUpdate{SessionInfoUpdate: &api.SessionSessionInfoUpdate{
		Meta: map[string]any{ctxloomSessionInfoMetaKey: u},
	}}
}

// usageUpdateWire projects one turn's completion accounting (TurnMeta) onto
// the real usage_update SessionUpdate variant (api.UsageUpdate/api.Cost) —
// the context-window gauge and cumulative cost. H1 confirmed this hand-rolled
// shape already matched the current spec exactly, so it is now built through
// api.SessionUpdate directly rather than a bespoke usageUpdate/usageCost pair.
// `used`/`size` follow ctxloom's own gauge convention (InputTokens against the
// model's ContextWindow; see run_structured.go), CostUSD is reported in USD.
// Returns nil when the turn carried no accounting worth reporting (so a bare
// completion — e.g. a cancel — emits no gauge). NOTE: returns the
// api.SessionUpdate by VALUE (never a nil *api.SessionUpdate) specifically so
// the "nothing to report" case can return a plain untyped nil `any` —
// emitUpdate's `update == nil` no-op check would otherwise be defeated by a
// typed-nil-in-interface (a non-nil interface wrapping a nil *SessionUpdate).
func usageUpdateWire(c *agent.TurnMeta) any {
	if c == nil || (c.InputTokens == 0 && c.ContextWindow == 0 && c.CostUSD == 0) {
		return nil
	}
	u := usageGaugeWire{SessionUpdate: usageUpdateKind}
	if c.InputTokens != 0 {
		used := c.InputTokens
		u.Used = &used
	}
	if c.ContextWindow != 0 {
		size := c.ContextWindow
		u.Size = &size
	}
	if c.CostUSD != 0 {
		u.Cost = &api.Cost{Amount: c.CostUSD, Currency: "USD"}
	}
	return u
}

// usageUpdateKind is the spec discriminator for the usage_update variant of
// the SessionUpdate oneOf.
const usageUpdateKind = "usage_update"

// usageGaugeWire is the usage_update frame, projected so an UNREPORTED number
// can be left out.
//
// schema-v1.19.0's UsageUpdate has no `required` array: `used` and `size` are
// both optional, and omitting one is how the spec says "no gauge". The
// generated api.SessionUsageUpdate declares them without omitempty, so it can
// only ever emit a number — which turns "the engine reported no token
// accounting" into the assertion "0 tokens of a 0-token window", a
// measurement nothing produced. Pointers restore the distinction between an
// unknown quantity and a zero one.
//
// A generated consumer decodes an absent optional field to the same zero it
// decoded an explicit 0 to, so this narrows what ctxloom CLAIMS without
// changing what any parser concludes.
type usageGaugeWire struct {
	SessionUpdate string    `json:"sessionUpdate"`
	Used          *int      `json:"used,omitempty"`
	Size          *int      `json:"size,omitempty"`
	Cost          *api.Cost `json:"cost,omitempty"`
}

// textBlock wraps text in an ACP text content block.
func textBlock(text string) api.ContentBlock {
	return api.TextBlock(text)
}

// rawValue decodes raw JSON to a plain value for the loosely-typed rawInput
// field (nil stays nil so omitempty drops it).
func rawValue(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	return v
}

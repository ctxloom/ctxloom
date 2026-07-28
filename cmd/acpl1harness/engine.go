package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Sentinel prompt texts driving the fake engine's scripted behavior. A driving
// L1 test sends one of these as the session/prompt text and asserts the
// EXACT resulting frame content — never just "no error" (this codebase's
// characteristic bug is a silent no-op, so every L1 assertion inspects real
// payload bytes).
const (
	// sentinelCancelMe blocks the turn until it receives a CancelTurn control
	// message, then completes with stopReason "cancelled" — the ordinary
	// mid-turn cancel path (server.go's runTurn cancelTurnCh branch).
	//
	// Unexported (U001-F05): this is a `main` package, which is unimportable
	// by construction, so exporting these bought no capability — the driving
	// test in internal/acpagent necessarily keeps its own hand-synced copies
	// (l1Sentinel*) rather than importing them. Match the already-unexported
	// replayUser/permToolName/terminalTestID below.
	sentinelCancelMe = "L1_CANCEL_ME"
	// sentinelRaceComplete waits for the server's forwarded CancelTurn (so
	// the test can deterministically prove the server-side cancel flag is
	// already set) but then deliberately IGNORES it and reports "end_turn"
	// anyway, simulating an engine that races session/cancel and finishes
	// normally regardless. The server MUST still report stopReason
	// "cancelled" (server.go:598-606's stopReason(), keyed on session/cancel
	// having arrived, not on what the engine itself says).
	sentinelRaceComplete = "L1_RACE_COMPLETE"
	// sentinelPermission announces a tool call, asks permission for it, and
	// reports the answer verbatim as the next entry's content — so a test can
	// assert selected/dismissed outcomes by inspecting real text.
	sentinelPermission = "L1_PERMISSION"
	// sentinelFail reports a fatal, fixed-text engine error instead of
	// completing, so a test can assert the resulting JSON-RPC error message
	// VERBATIM (see failMessage).
	sentinelFail = "L1_FAIL"
	// sentinelTerminal (B1, gap G6) asks the server to broker one
	// terminal/create call to the connected editor, then reports the
	// editor's real answer (terminalId on success, the error text on
	// failure) verbatim as the next entry's content — so a test can assert
	// the exact bytes the editor sent survived the round trip across the
	// real process boundary, in both directions.
	sentinelTerminal = "L1_TERMINAL"

	// failMessage is sentinelFail's exact error text — pinned as a constant
	// so the driving test's verbatim assertion and this engine can never
	// silently drift apart.
	failMessage = "l1 harness: deliberate failure for conformance testing"

	// replayUser/replayAssistant are the FIXED session/load replay history
	// this harness returns for any resumed harp, so a test can assert the
	// replayed session/update content verbatim.
	replayUser      = "earlier question (l1 harness replay)"
	replayAssistant = "earlier answer (l1 harness replay)"

	// permToolName is the tool name shared by sentinelPermission's
	// announced tool_use and its permission request, matching the real
	// server's FIFO tool-call-id pairing convention (acpagent/mapping.go).
	permToolName = "l1_tool"

	// terminalTestID is sentinelTerminal's fixed TerminalRequest.ID.
	terminalTestID = "l1-term-1"
	// terminalTestParams is sentinelTerminal's fixed, ALREADY-STRIPPED (no
	// sessionId — the real client-role driver strips it; see
	// internal/acp/session.go's stripSessionID) terminal/create body, so a
	// driving test can assert the server injects its OWN session id rather
	// than carrying this engine's opaque one through.
	terminalTestParams = `{"command":"echo","args":["hi"]}`
)

// sessionSeq mints the harp for a fresh (non-resumed) session — deterministic
// and process-local, matching the fallback convention EngineChat.Harp
// documents (a connection-local generated id when accounting is
// unavailable), except here it's an intentional test id, not a fallback.
var sessionSeq int64

// openHarnessEngine is the acpagent.ChatOpener behind the L1 harness binary.
// It never touches ctxloom config, credentials, or a real LLM: it is a small
// deterministic state machine keyed on the incoming prompt TEXT (see the
// Sentinel* consts). A resumed session (session/load) always replays the
// SAME fixed two-entry history regardless of the requested harp name — the
// harness cares about replay ORDERING and CONTENT, not persistence.
func openHarnessEngine(ctx context.Context, req acpagent.OpenRequest) (*acpagent.EngineChat, error) {
	in := make(chan agent.ChatMessage, 4)
	events := make(chan agent.ChatEvent, 16)
	errs := make(chan error, 1)
	closed := make(chan struct{})

	engineCtx, cancel := context.WithCancel(ctx)

	var replay []agent.SessionEntry
	harp := req.ResumeHarp
	if harp != "" {
		replay = []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: replayUser},
			{Type: agent.EntryTypeAssistant, Content: replayAssistant},
		}
	} else {
		n := atomic.AddInt64(&sessionSeq, 1)
		harp = "l1-harness-" + strconv.FormatInt(n, 10)
	}

	go runHarnessEngine(engineCtx, in, events, errs)

	closeOnce := func() {
		cancel()
		select {
		case <-closed:
		default:
			close(closed)
			close(events)
		}
	}

	return &acpagent.EngineChat{
		Context: "", // no lead context — this harness tests the ACP protocol layer, not context assembly
		In:      in,
		Events:  events,
		Errs:    errs,
		Close:   closeOnce,
		Harp:    harp,
		Replay:  replay,
	}, nil
}

// runHarnessEngine reads one user message per turn from `in` and scripts the
// response; it loops for the session's next turn once a turn completes.
func runHarnessEngine(ctx context.Context, in <-chan agent.ChatMessage, events chan<- agent.ChatEvent, errs chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-in:
			if !ok {
				return
			}
			if msg.CancelTurn || msg.Permission != nil || msg.Terminal != nil {
				continue // stray control message with no turn in flight
			}
			runTurn(ctx, msg.Text, in, events, errs)
		}
	}
}

// runTurn scripts exactly one turn's events by the prompt text's sentinel
// (or the default echo behavior for ordinary text).
func runTurn(ctx context.Context, text string, in <-chan agent.ChatMessage, events chan<- agent.ChatEvent, errs chan<- error) {
	switch text {
	case sentinelCancelMe:
		if !emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "waiting for cancel"}}) {
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-in:
				if !ok {
					return
				}
				if m.CancelTurn {
					emit(ctx, events, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "cancelled"}})
					return
				}
			}
		}
	case sentinelRaceComplete:
		if !emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "finishing before any cancel is read"}}) {
			return
		}
		// Deterministically wait for the server's FORWARDED CancelTurn (this
		// proves the server-side turnCancelled flag is already set — the
		// same synchronization TestServe_CancelRace uses in-process), then
		// deliberately IGNORE it and report "end_turn" anyway: the engine
		// races the cancel and finishes normally regardless. The server
		// MUST still resolve the prompt as "cancelled" (server.go:598-606's
		// stopReason(), keyed on session/cancel having arrived — not on
		// what the engine itself reports).
		for {
			select {
			case <-ctx.Done():
				return
			case m, ok := <-in:
				if !ok {
					return
				}
				if m.CancelTurn {
					emit(ctx, events, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
					return
				}
			}
		}
	case sentinelPermission:
		if !emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeToolUse, ToolName: permToolName}}) {
			return
		}
		if !emit(ctx, events, agent.ChatEvent{Permission: &agent.PermissionRequest{
			ID:       "l1-perm-1",
			ToolName: permToolName,
			Options: []agent.PermissionOption{
				{ID: "allow", Kind: "allow_once", Name: "Allow"},
				{ID: "deny", Kind: "reject_once", Name: "Reject"},
			},
		}}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case m, ok := <-in:
			switch {
			case !ok || m.Permission == nil:
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "permission: no answer"}})
			case m.Permission.OptionID == "":
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "permission: dismissed"}})
			default:
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "permission: " + m.Permission.OptionID}})
			}
		}
		emit(ctx, events, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	case sentinelFail:
		select {
		case errs <- errors.New(failMessage):
		case <-ctx.Done():
		}
	case sentinelTerminal:
		if !emit(ctx, events, agent.ChatEvent{Terminal: &agent.TerminalRequest{
			ID:     terminalTestID,
			Op:     agent.TerminalOpCreate,
			Params: json.RawMessage(terminalTestParams),
		}}) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case m, ok := <-in:
			switch {
			case !ok || m.Terminal == nil:
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "terminal: no answer"}})
			case m.Terminal.Error != "":
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "terminal error: " + m.Terminal.Error}})
			default:
				emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "terminal result: " + string(m.Terminal.Result)}})
			}
		}
		emit(ctx, events, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	default:
		if !emit(ctx, events, agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "echo: " + text}}) {
			return
		}
		emit(ctx, events, agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}})
	}
}

// emit sends one event, returning false if ctx died first (session torn down
// mid-turn — the caller should stop scripting further events).
func emit(ctx context.Context, events chan<- agent.ChatEvent, ev agent.ChatEvent) bool {
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

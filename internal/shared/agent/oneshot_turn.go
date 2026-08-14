package agent

import (
	"fmt"
	"io"
	"maps"
	"strings"
)

// ScrubInternalIdentityEnv returns a copy of env with SessionHarpEnv forced
// empty. It is the ONE lever available on the StructuredChat-backed oneshot
// Execute path (internal/acp's ACP.Execute, internal/opencode's
// Opencode.Execute) to neutralize a ctxloom-authored SessionStart hook
// (session-bind, inject-context) that the spawned engine's OWN settings
// still register: unlike claude-code's native buildArgs (minimalSettings,
// `--settings '{"hooks":{}}'`), this path spawns a real ACP adapter whose
// settings ctxloom does not control — ACP's own CellDelivery writes nothing
// (NewACP's doc), so there is no settings file to override. Every
// ctxloom-authored hook keys off SessionHarpEnv and no-ops when it reads it
// empty (session_bind.go's `if harp == "" { return nil }`), so the hook
// still fires but produces no lineage-observable effect — the same outcome
// claude's minimalSettings reaches by removing the hook registration
// outright.
//
// Callers apply this ONLY for an internal, non-user-initiated invocation
// (ExecuteRequest.SkipSetup — distillation/compaction is the only caller
// today). A real delegated child (agent_run) or an interactive `ctxloom acp`
// session must keep its own harp reaching the engine, so this must never run
// unconditionally on the Chat path. env is never mutated; the returned map
// is a fresh copy so the caller's own map (often shared, e.g. req.Env) is
// unaffected.
func ScrubInternalIdentityEnv(env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[SessionHarpEnv] = ""
	return out
}

// RunOneshotTurn drives one complete oneshot ACP-shaped chat turn — the drain/
// render plumbing internal/acp's Execute and internal/opencode's Execute both
// carried duplicated (byte-for-byte, per reprise's own duplicate-detection
// gate), including the same defect:
//
//   - an empty (or whitespace-only) prompt was sent to the engine with no
//     check at all;
//   - a turn producing zero assistant text exited 0 with zero bytes written
//     and no diagnostic — this project's characteristic silent-noop shape,
//     "exit 0, success message, zero bytes delivered".
//
// Both are fixed here, once, for every caller. An empty prompt now refuses
// before the engine is ever invoked. A textless turn still exits 0 (a
// tool-only turn is a legitimate, if unusual, outcome — this is not a hard
// failure) but now warns on stderr instead of staying silent.
//
// runChat is the caller's own backend.Chat closure — its ChatRequest fields
// (MCPServers, Permissions, etc.) differ per backend; the turn plumbing
// around it does not.
func RunOneshotTurn(prompt *Fragment, modelInfo *ModelInfo, verbosity uint32, stdout, stderr io.Writer,
	runChat func(in <-chan ChatMessage, out chan<- ChatEvent) error) (*ExecuteResult, error) {
	content := GetPromptContent(prompt)
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("empty prompt: nothing to send for this turn")
	}

	in := make(chan ChatMessage, 1)
	in <- ChatMessage{Text: content}
	close(in)
	// Buffered so the drain loop below and the driver never deadlock on the
	// final events emitted between the last read and the channel close.
	out := make(chan ChatEvent, 16)

	done := make(chan error, 1)
	go func() { done <- runChat(in, out) }()

	// Render the turn: assistant chunks stream to stdout (concatenated — the
	// mapper emits one entry per chunk); thinking and tool traffic go to
	// stderr only at verbosity, keeping stdout clean for the response text.
	wroteText := false
	// stopReason is captured from the turn's Complete event so a
	// textless turn's warning can say WHY — a refusal, a max_tokens cutoff,
	// and a cancelled turn are different failures with different fixes, and
	// this used to collapse all of them (plus a genuinely empty tool-only
	// answer) into the same silent "no assistant text" message.
	var stopReason string
	for ev := range out {
		if ev.Complete != nil {
			stopReason = ev.Complete.StopReason
		}
		if ev.Entry == nil {
			continue
		}
		switch ev.Entry.Type {
		case EntryTypeAssistant:
			fmt.Fprint(stdout, ev.Entry.Content)
			wroteText = true
		case EntryTypeThinking:
			if verbosity >= 16 {
				fmt.Fprintf(stderr, "[thinking] %s\n", ev.Entry.Content)
			}
		case EntryTypeToolUse:
			if verbosity >= 16 {
				fmt.Fprintf(stderr, "[tool] %s\n", ev.Entry.ToolName)
			}
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if wroteText {
		fmt.Fprintln(stdout)
	} else if stopReason != "" {
		// Named cause: a client reading stderr can tell "refused"/"cancelled"/
		// a token-limit cutoff apart from an ordinary tool-only turn, instead
		// of seeing the identical message for all of them.
		fmt.Fprintf(stderr, "ctxloom: warning: this turn produced no assistant text (stop reason: %s)\n", stopReason)
	} else {
		fmt.Fprintln(stderr, "ctxloom: warning: this turn produced no assistant text")
	}
	return &ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
}

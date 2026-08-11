package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/harpmarker"
)

// The `ctxloom hook session-bind` machinery: a machine callback fired by an
// engine's SessionStart hook. It is NOT part
// of the user-facing `session` command tree — it registers under the hidden
// `hook` namespace — and it answers to a different contract: stdout carries
// hook output only, and nothing here may ever fail the host engine's startup.

func init() {
	// session-bind is a machine callback (SessionStart hook target), so it lives
	// under the hidden `hook` namespace, not the user-facing `session` one.
	hookCmd.AddCommand(sessionBindCmd)
}

// sessionBindCmd is the session-bind hook target and the sole path for
// recording the harp → session_id mapping in the index. Claude Code (and
// other backends with SessionStart hooks) fire this once per TRANSCRIPT, not
// once per session: /clear rotates the transcript to a new UUID under the same
// live process and the hook fires again with the new one. The payload carries
// the backend's session ID and transcript path, so a repeat firing that names
// a new transcript re-points the binding and one that names the same
// transcript is a no-op. The compactor also forward-binds at compact time as a
// backstop.
var sessionBindCmd = &cobra.Command{
	Use:    "session-bind",
	Short:  "Bind the current backend session to the active harp (internal — used by the SessionStart hook)",
	Hidden: true,
	RunE:   runSessionBind,
}

func runSessionBind(cmd *cobra.Command, args []string) error {
	harp := os.Getenv("CTXLOOM_SESSION_HARP")
	// Read the hook payload once: the marker doesn't need it, the bind does.
	raw, _ := io.ReadAll(cmd.InOrStdin())
	// Emit the deterministic harp self-id marker as SessionStart context so
	// the transcript carries a greppable owner tag, independent of the index,
	// the binding, or PID bookkeeping. This is the SessionStart hook installed
	// for every ctxloom session, so it identifies the harp even when no
	// project context (inject-context) is configured. Best-effort: a hook must
	// never fail the host backend's startup, so failures past this point only
	// skip the index bind — the marker is already on stdout.
	emitHarpMarker(cmd.OutOrStdout(), harp)
	if err := bindSessionFromPayload(bytes.NewReader(raw), harp); err != nil {
		clidiag.Warn("ctxloom", "session bind failed: %v", err)
	}
	return nil
}

// emitHarpMarker writes the harp self-id marker to w as a SessionStart hook
// output (the same envelope inject-context uses), so the backend injects it into
// the session and it lands in the transcript.
//
// The marker is the only index-independent statement of which harp owns a
// transcript, so a run that emits none produces a transcript nothing can
// attribute. Emitting it is still best-effort — a hook must never fail the host
// backend's startup — but every way of emitting nothing is REPORTED on the
// diagnostic channel. stdout stays the hook's contract channel and never
// carries a diagnostic.
func emitHarpMarker(w io.Writer, harp string) {
	marker := harpmarker.Format(harp)
	if marker == "" {
		clidiag.Warn("ctxloom", "session-bind: no usable harp (CTXLOOM_SESSION_HARP=%q) — this session's transcript carries no harp self-id marker and cannot be attributed to a harp by content", harp)
		return
	}
	out := HookOutput{HookSpecificOutput: &HookSpecificOutput{
		HookEventName:     claude.HookEventSessionStart,
		AdditionalContext: marker,
	}}
	b, err := json.Marshal(out)
	if err != nil {
		clidiag.Warn("ctxloom", "session-bind: harp %q: could not encode the harp self-id marker: %v — the transcript carries no owner tag", harp, err)
		return
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		clidiag.Warn("ctxloom", "session-bind: harp %q: could not write the harp self-id marker: %v — the transcript carries no owner tag", harp, err)
	}
}

// bindSessionFromPayload reads a SessionStart hook payload from in,
// extracts session_id / transcript_path, and binds them to harp in the
// given Manager. Idempotent: re-running with the same payload is a
// no-op. Malformed payloads silently succeed (a hook must never fail
// the host backend's startup over a bad message).
//
// Extracted from sessionBindCmd's RunE so the binding logic is testable
// without spinning up cobra or the real os.Stdin.
func bindSessionFromPayload(in io.Reader, harp string) error {
	if harp == "" {
		return nil
	}
	raw, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read payload: %w", err)
	}
	var payload claude.SessionStartPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		// This must never fail the host backend's tool call over a
		// bad hook message (returning nil is right), but a malformed payload
		// silently skipping the harp->session_id bind with NOTHING reported
		// anywhere left an operator no way to learn why a harp never got
		// captured — one of the two live-reproducible causes behind
		// "no canonical transcript captured for harp ...".
		clidiag.Warn("ctxloom", "session-bind: harp %q: SessionStart hook payload did not parse as JSON: %v — harp<->session_id bind skipped", harp, err)
		return nil
	}
	// Confirmed live 2026-07-21 against real kiro-cli 2.12.1:
	// kiro's agentSpawn hook stdin payload carries NO session identifier at
	// all ({"hook_event_name":"agentSpawn","cwd":...,"prompt":...} — no
	// session_id/conversation_id field), unlike Claude/Codex's
	// payloads. It DOES set KIRO_SESSION_ID in the hook subprocess's OWN
	// environment, confirmed (by direct sqlite query against the real
	// conversations_v2 table) to equal that conversation's actual
	// conversation_id. Falling back to it here means locateKiroConversation
	// (vendorreader_kiro.go) hits its SessionID-bound fast path — an exact
	// match, not the best-effort enumerate-by-workdir heuristic — for every
	// ctxloom-launched kiro session, since the agentSpawn hook always fires.
	if payload.SessionID == "" {
		payload.SessionID = os.Getenv("KIRO_SESSION_ID")
	}
	// operations.BindSession no-ops a harp that is absent from the index, and
	// re-points one whose engine has rotated to a new transcript.
	return operations.BindSession(harp, payload.SessionID, payload.TranscriptPath)
}

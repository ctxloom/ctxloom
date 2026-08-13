//go:build acceptance

// Steps for j000900_context_exhaustion.feature: what a developer sees when they run
// out of context mid-task and reach for /clear + /recover.
//
// The SessionStart hook is the whole discovery half of that journey, and the
// mechanism it turns on is one field. Claude Code reports WHY a session started
// — startup|resume|clear|compact — so ctxloom can tell a cleared conversation
// from a compacted one and speak up only for the first. That distinction is the
// journey's subject, so these steps drive the real hook with a real source
// rather than asserting on prose about it.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

// systemMessageEnvelope is the half of the SessionStart output that reaches the
// USER rather than the model. Declared separately from steps_session_hooks.go's
// hookEnvelope for the reason that file gives for declaring its own: this is a
// contract with a third party, and a struct shared with internal/cli would let
// both ends move together without anything failing.
type systemMessageEnvelope struct {
	SystemMessage string `json:"systemMessage"`
}

// hookSystemMessage parses the last command's stdout for the user-facing
// channel. Parsing is itself an assertion: stdout is the engine's input, so
// anything unparseable there is a corrupted payload rather than a message.
func hookSystemMessage(w *World) (string, error) {
	out := strings.TrimSpace(w.env.LastStdout())
	if out == "" {
		return "", fmt.Errorf("the hook wrote nothing to stdout, so the engine received no envelope at all")
	}
	var env systemMessageEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return "", fmt.Errorf("the hook's stdout is not the JSON envelope the engine parses (%v); stdout:\n%s", err, out)
	}
	return env.SystemMessage, nil
}

// liveTranscriptPath is where these steps park the still-growing transcript, so
// the "session start" steps below can hand the hook a real path to stat without
// the feature file having to spell one out. No longer what decides
// recoverability (see j000900SessionIDAfterClear's doc comment) — kept as
// realistic scene-setting for the hookInput.TranscriptPath field a host
// engine always sends.
const liveTranscriptRel = "live-session-transcript.jsonl"

// j000900Harp is the harp these scenarios bind their session-index fixture
// entries to. CTXLOOM_SESSION_HARP is how currentSessionRecoverable finds the
// entry (the same env var production reads — internal/shared/agent.
// SessionHarpEnv), so every scenario that wants a specific recoverability
// outcome must set it via SetChildEnv (a plain SetEnv is stripped — see
// steps_session_hooks.go's comment on the ambient-session scrub list).
const j000900Harp = "dana-context-exhaustion"

// j000900SessionIDAfterClear is the session id startSessionAfter's payload
// always carries — the NEW id claude-code's /clear reports on its
// SessionStart. currentSessionRecoverable's gate (post rotation-lineage fix)
// reads the session INDEX, not the transcript file: at inject-context time
// the index still holds Dana's PRE-clear binding, because claude.go's
// ctxloomMachineCallbacks runs inject-context BEFORE session-bind — the bind
// that would append the rotation and re-point SessionID to this value hasn't
// run yet. So recoverability turns on whether the index entry's CURRENT
// SessionID differs from this one (a displacement about to happen), not on
// any transcript file's content.
const j000900SessionIDAfterClear = "vendor-session-j000900"

// startSessionAfter runs the SessionStart hook the way a host engine does:
// input on stdin, naming why the session started. transcript is the path the
// engine reports for the conversation.
func startSessionAfter(w *World, source, transcript string) error {
	payload, err := json.Marshal(map[string]string{
		"session_id":      j000900SessionIDAfterClear,
		"hook_event_name": "SessionStart",
		"source":          source,
		"transcript_path": transcript,
	})
	if err != nil {
		return err
	}
	// A context hash that resolves to nothing: this journey is about the
	// user-facing channel, and a hook that also delivered context would let a
	// scenario pass on the wrong half of the envelope.
	return w.env.RunWithStdin(string(payload), "hook", "inject-context", "no-such-context")
}

// seedJ000900IndexBinding writes a session-index entry for j000900Harp,
// bound to sessionID, and points CTXLOOM_SESSION_HARP at it — the fixture
// shape currentSessionRecoverable actually reads.
func seedJ000900IndexBinding(w *World, sessionID string) error {
	w.env.SetChildEnv("CTXLOOM_SESSION_HARP", j000900Harp)
	entry := fmt.Sprintf("sessions:\n"+
		"  - harp_name: %s\n"+
		"    backend: mock\n"+
		"    project_dir: %s\n"+
		"    session_id: %s\n"+
		"    started_at: 2026-03-14T00:00:00Z\n", j000900Harp, w.env.ProjectDir, sessionID)
	return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", entry)
}

func registerJ000900RecoverSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Dana has been working long enough that her context is nearly full$`, func(c context.Context) error {
		w := worldFrom(c)
		// The index entry carries Dana's PRE-clear session id — a different id
		// from the one the clear payload below will carry
		// (j000900SessionIDAfterClear). That difference IS the recoverable
		// signal at inject-context time (see j000900SessionIDAfterClear's doc
		// comment): a bound entry whose current SessionID differs from the
		// incoming one is a displacement about to be recorded.
		return seedJ000900IndexBinding(w, "vendor-session-j000900-preclear")
	})

	ctx.Step(`^Dana's session has recorded nothing worth recovering$`, func(c context.Context) error {
		w := worldFrom(c)
		// Bound, but to the SAME id the clear payload will carry — an
		// idempotent rebind, not a displacement. Nothing was thrown away, so
		// there is nothing to offer to bring back.
		return seedJ000900IndexBinding(w, j000900SessionIDAfterClear)
	})

	ctx.Step(`^her assistant starts a fresh turn after she clears the context$`, func(c context.Context) error {
		w := worldFrom(c)
		return startSessionAfter(w, "clear", filepath.Join(w.env.ProjectDir, liveTranscriptRel))
	})

	ctx.Step(`^her assistant starts a fresh turn after the harness compacts the conversation$`, func(c context.Context) error {
		w := worldFrom(c)
		return startSessionAfter(w, "compact", filepath.Join(w.env.ProjectDir, liveTranscriptRel))
	})

	ctx.Step(`^her assistant starts a fresh turn after she clears a session with no transcript at all$`, func(c context.Context) error {
		w := worldFrom(c)
		gone := filepath.Join(w.env.ProjectDir, "never-written.jsonl")
		if _, err := os.Stat(gone); err == nil {
			return fmt.Errorf("fixture error: %s exists, so this scenario is not testing a missing transcript", gone)
		}
		return startSessionAfter(w, "clear", gone)
	})

	ctx.Step(`^Dana is told her cleared context can be brought back$`, func(c context.Context) error {
		w := worldFrom(c)
		msg, err := hookSystemMessage(w)
		if err != nil {
			return err
		}
		// The command a reader has to type is the load-bearing part: a nudge
		// that says "context cleared" without naming /recover tells someone
		// something has gone wrong and nothing about what to do next.
		if !strings.Contains(msg, "/recover") {
			return fmt.Errorf("the user was not pointed at /recover; systemMessage was %q", msg)
		}
		return nil
	})

	ctx.Step(`^Dana is told nothing about recovery$`, func(c context.Context) error {
		w := worldFrom(c)
		msg, err := hookSystemMessage(w)
		if err != nil {
			return err
		}
		if strings.Contains(msg, "/recover") {
			return fmt.Errorf("the user was nudged toward /recover when there was nothing to recover; systemMessage was %q", msg)
		}
		return nil
	})
}

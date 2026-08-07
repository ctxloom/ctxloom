//go:build acceptance

// The four hidden machine callbacks (session_hooks.feature): `hook
// inject-context`, `hook session-bind`, `hook stamp-plan` and `hook hud`.
//
// Every assertion about what a hook DELIVERS reads stdout alone, never the
// combined stream. That is not fastidiousness: stdout is the host engine's
// input, so a diagnostic printed there is not a visible warning but a
// corrupted payload, and a test reading the combined stream would pass while
// ctxloom spliced a warning line into an engine's context envelope. Parsing
// stdout as JSON is what makes that failure loud here.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

// hookEnvelope is the SessionStart hook output shape both inject-context and
// session-bind emit. Declared here rather than imported from internal/cli so a
// change to the wire shape shows up as a deliberate update on the test side
// too — this is the contract with a THIRD party (the host engine), and a
// shared struct would let both ends move together without anything failing.
type hookEnvelope struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// hookStdoutEnvelope parses the last command's STDOUT as the hook envelope.
// The parse itself is an assertion: stdout that does not parse means a
// diagnostic (or anything else) leaked onto the engine's input channel.
func hookStdoutEnvelope(w *World) (hookEnvelope, error) {
	var env hookEnvelope
	out := strings.TrimSpace(w.env.LastStdout())
	if out == "" {
		return env, fmt.Errorf("the hook wrote nothing to stdout, so the engine received no envelope at all")
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		return env, fmt.Errorf("the hook's stdout is not the JSON envelope the engine parses (%v); stdout:\n%s", err, out)
	}
	return env, nil
}

func registerSessionHookSteps(ctx *godog.ScenarioContext) {
	// The harp reaches a hook the way it reaches one in production: through
	// the environment the host engine's subprocess inherits, not a flag.
	//
	// SetChildEnv rather than SetEnv, and the difference is load-bearing:
	// CTXLOOM_SESSION_HARP is on the ambient-session scrub list, so a plain
	// SetEnv is stripped before the child ever sees it and every assertion
	// below would fail for a reason that has nothing to do with the hook.
	// SetChildEnv is the deliberate door through that scrub.
	ctx.Step(`^the session harp is "([^"]*)"$`, func(c context.Context, harp string) error {
		worldFrom(c).env.SetChildEnv("CTXLOOM_SESSION_HARP", harp)
		return nil
	})

	// An index entry with NO session_id, which is the only state a bind can
	// actually change: operations.BindSession is first-bind-wins and no-ops a
	// harp that is absent OR already bound, so seeding a bound entry (what
	// j23's own helper writes, since its scenarios need bindings that already
	// exist) would make the bind a no-op and the assertion below vacuous.
	ctx.Step(`^the session index has an unbound entry for harp "([^"]*)"$`, func(c context.Context, harp string) error {
		w := worldFrom(c)
		entry := fmt.Sprintf("sessions:\n"+
			"  - harp_name: %s\n"+
			"    backend: mock\n"+
			"    project_dir: %s\n"+
			"    started_at: 2026-03-14T00:00:00Z\n", harp, w.env.ProjectDir)
		return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", entry)
	})

	ctx.Step(`^the session index binds harp "([^"]*)" to session "([^"]*)"$`, func(c context.Context, harp, sessionID string) error {
		w := worldFrom(c)
		body, err := w.env.ReadHomeFile(".ctxloom/sessions/index.yaml")
		if err != nil {
			return fmt.Errorf("read the session index: %w", err)
		}
		var doc struct {
			Sessions []struct {
				HarpName       string `yaml:"harp_name"`
				SessionID      string `yaml:"session_id"`
				TranscriptPath string `yaml:"transcript_path"`
			} `yaml:"sessions"`
		}
		if err := yaml.Unmarshal([]byte(body), &doc); err != nil {
			return fmt.Errorf("parse the session index: %w; index:\n%s", err, body)
		}
		for _, s := range doc.Sessions {
			if s.HarpName != harp {
				continue
			}
			if s.SessionID != sessionID {
				return fmt.Errorf("harp %q is bound to session %q, not %q — the hook exited 0 without recording the binding; index:\n%s", harp, s.SessionID, sessionID, body)
			}
			return nil
		}
		return fmt.Errorf("the index holds no entry for harp %q at all; index:\n%s", harp, body)
	})

	ctx.Step(`^the hook's additionalContext contains "([^"]*)"$`, func(c context.Context, want string) error {
		env, err := hookStdoutEnvelope(worldFrom(c))
		if err != nil {
			return err
		}
		if !strings.Contains(env.HookSpecificOutput.AdditionalContext, want) {
			return fmt.Errorf("the context handed to the engine does not contain %q; it carried:\n%s", want, env.HookSpecificOutput.AdditionalContext)
		}
		return nil
	})

	// The counterpart of the step above, and not a weaker version of it: the
	// envelope must still be well-formed JSON (the engine parses it either
	// way) while carrying nothing, which is what distinguishes "degraded
	// cleanly" from both "delivered" and "emitted garbage".
	ctx.Step(`^the hook's additionalContext is empty$`, func(c context.Context) error {
		env, err := hookStdoutEnvelope(worldFrom(c))
		if err != nil {
			return err
		}
		if got := env.HookSpecificOutput.AdditionalContext; got != "" {
			return fmt.Errorf("expected no context to be delivered, but the envelope carried:\n%s", got)
		}
		return nil
	})

	// Silence on stdout is a POSITIVE requirement here, not the absence of a
	// check: an engine injects whatever arrives on this channel verbatim, so a
	// marker naming an empty harp would be written into the transcript as
	// fact. Diagnostics on stderr are expected and deliberately not counted.
	ctx.Step(`^the hook writes nothing to stdout$`, func(c context.Context) error {
		w := worldFrom(c)
		if out := strings.TrimSpace(w.env.LastStdout()); out != "" {
			return fmt.Errorf("the hook wrote to the engine's input channel when it had nothing truthful to say; stdout:\n%s", out)
		}
		return nil
	})
}

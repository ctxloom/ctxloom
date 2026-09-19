//go:build acceptance

// The hidden machine callbacks (session_hooks.feature): `hook
// inject-context`, `hook session-bind`, `hook stamp-plan`, `hook hud` and
// `hook turn-changed`.
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
	"os"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
	"gopkg.in/yaml.v3"
)

// hookEnvelope is the SessionStart hook output shape both inject-context and
// session-bind emit. Declared here rather than imported from internal/adapters/cli so a
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

	// A session record with NO session_id, which is the only state a bind can
	// actually change: operations.BindSession is first-bind-wins and no-ops a
	// harp that is absent OR already bound, so seeding a bound entry (what
	// j001200's own helper writes, since its scenarios need bindings that already
	// exist) would make the bind a no-op and the assertion below vacuous.
	ctx.Step(`^the session index has an unbound entry for harp "([^"]*)"$`, func(c context.Context, harp string) error {
		return seedSessionSidecar(worldFrom(c), harp, sessionSeed{
			Backend:   "mock",
			StartedAt: "2026-03-14T00:00:00Z",
		})
	})

	ctx.Step(`^the session index binds harp "([^"]*)" to session "([^"]*)"$`, func(c context.Context, harp, sessionID string) error {
		w := worldFrom(c)
		// The binding lives in the harp's own sidecar now, not a shared index.
		body, err := w.env.ReadHomeFile(".ctxloom/sessions/" + harp + "/session.yaml")
		if err != nil {
			return fmt.Errorf("read the session record for harp %q: %w", harp, err)
		}
		var sidecar struct {
			SessionID      string `yaml:"session_id"`
			TranscriptPath string `yaml:"transcript_path"`
		}
		if err := yaml.Unmarshal([]byte(body), &sidecar); err != nil {
			return fmt.Errorf("parse the session record for %q: %w; record:\n%s", harp, err, body)
		}
		if sidecar.SessionID != sessionID {
			return fmt.Errorf("harp %q is bound to session %q, not %q — the hook exited 0 without recording the binding; record:\n%s", harp, sidecar.SessionID, sessionID, body)
		}
		return nil
	})

	// The launch resolver mints the session's MCP endpoint once per harp and
	// binds it on the session record, so a resume of the same harp reuses
	// it. Every session the run minted must carry one: the scan refuses a
	// home with no session record at all (a run that minted nothing would
	// otherwise pass vacuously).
	ctx.Step(`^the run's session record carries its MCP endpoint$`, func(c context.Context) error {
		w := worldFrom(c)
		root := filepath.Join(w.env.HomeDir, filepath.FromSlash(harpSessionsRel))
		entries, err := os.ReadDir(root)
		if err != nil {
			return fmt.Errorf("read the session store at %s: %w", root, err)
		}
		checked := 0
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			body, err := os.ReadFile(filepath.Join(root, e.Name(), "session.yaml"))
			if err != nil {
				continue
			}
			var sidecar struct {
				MCP struct {
					URL        string `yaml:"url"`
					Credential string `yaml:"credential"`
				} `yaml:"mcp"`
			}
			if err := yaml.Unmarshal(body, &sidecar); err != nil {
				return fmt.Errorf("parse the session record for %q: %w; record:\n%s", e.Name(), err, body)
			}
			if sidecar.MCP.URL == "" || sidecar.MCP.Credential == "" {
				return fmt.Errorf("session %q records no MCP endpoint — the launch resolver mints one per harp and binds it on the record; record:\n%s", e.Name(), body)
			}
			checked++
		}
		if checked == 0 {
			return fmt.Errorf("no session record under %s to check — the run minted no session", root)
		}
		return nil
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
	// turn-changed's ENTIRE result is one word on stdout; its exit status is
	// always 0 and carries no meaning. So this compares stdout ALONE, exactly:
	// a substring check would let "unchanged" satisfy a test expecting
	// "changed", which is the precise inversion the close-out contract depends
	// on. Diagnostics belong on stderr and are deliberately not read here.
	ctx.Step(`^the turn verdict on stdout is "([^"]*)"$`, func(c context.Context, want string) error {
		w := worldFrom(c)
		got := strings.TrimSpace(w.env.LastStdout())
		if got != want {
			return fmt.Errorf("turn verdict: want %q, got %q (stdout alone)", want, got)
		}
		return nil
	})

	ctx.Step(`^the hook writes nothing to stdout$`, func(c context.Context) error {
		w := worldFrom(c)
		if out := strings.TrimSpace(w.env.LastStdout()); out != "" {
			return fmt.Errorf("the hook wrote to the engine's input channel when it had nothing truthful to say; stdout:\n%s", out)
		}
		return nil
	})
}

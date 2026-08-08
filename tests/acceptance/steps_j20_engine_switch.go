//go:build acceptance

// J20: "engine switch day" (j20_engine_switch.feature) — FLOWS-UNIFIED.md's
// U12.
//
// THE FINDING THIS JOURNEY EXISTS TO PIN is not a missing capability, it is a
// missing FLOW. Every piece of engine substitution is built: `agent edit
// --engine` swaps the binding, profiles are engine-neutral by construction,
// each engine's native surfaces are written from the same composed context,
// and the canonical transcript keeps history portable across the switch.
// Nothing here is structural for the mainstream engines. What does not exist
// is any narration of ORDER or VERIFICATION — nothing tells a team what to do
// first, what to check afterwards, or what they just lost. The portability
// story is sellable and untold, and that gap is what these scenarios measure.
//
// SEAM WITH J4. J4 (j4_multi_engine.feature) is the axis's REFERENCE feature:
// one profile, many engines, a matrix row per engine. It is deliberately NOT a
// journey and this file does not turn it into one. J20 asserts the things a
// matrix cannot express — that the swap is a SEQUENCE with a before and an
// after, that the context surviving it is the SAME context rather than merely
// a valid one, and that the validation gaps show up exactly where a migrating
// team would hit them.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cucumber/godog"
)

const (
	// j20Marker is the team's guidance — one string, asserted byte-identical
	// on both sides of the switch. Sameness is the whole claim: an engine
	// swap that delivers DIFFERENT-but-valid content is a migration that
	// silently changed what the assistant knows.
	j20Marker = "J20-TEAM-GUIDANCE-MARKER"

	j20OldEngine = "claude-code"
	j20NewEngine = "codex"

	// j20Agent is the binding the whole team runs through — the thing the
	// switch is performed ON.
	j20Agent = "dev"

	// j20HookCommand is the team's session_end hook — real config the switch
	// actually costs something against. claude-code (the old engine) carries
	// session_end; codex (the new one) has no native session-end event
	// (codex.NoSessionEndReason), so this is what "what did the team just
	// give up" measures concretely.
	j20HookCommand = "echo j20-session-end"
)

// j20State is this journey's fixture state.
type j20State struct {
	ready bool
	// before/after hold the composed context materialized on each side of the
	// switch, so the comparison is between two real materializations rather
	// than between one and an expectation.
	before string
	after  string
}

func j20Of(w *World) *j20State {
	if w.j20 == nil {
		w.j20 = &j20State{}
	}
	return w.j20
}

// j20Config renders a project declaring BOTH engines up front. A migration is
// a swap between two configured engines; a fixture with only the destination
// configured would be testing initial setup instead.
func j20Config() string {
	var b strings.Builder
	b.WriteString("version: 4\n")
	b.WriteString("llm:\n  configs:\n")
	fmt.Fprintf(&b, "    %s:\n      type: %s\n", j20OldEngine, j20OldEngine)
	fmt.Fprintf(&b, "    %s:\n      type: %s\n", j20NewEngine, j20NewEngine)
	fmt.Fprintf(&b, "  defaults:\n    primary: %s\n    fast: %s\n", j20OldEngine, j20OldEngine)
	fmt.Fprintf(&b, "agents:\n  %s:\n    engine: %s\n    profiles:\n      - default\n", j20Agent, j20OldEngine)
	fmt.Fprintf(&b, "default_agent: %s\n", j20Agent)
	return b.String()
}

// j20Setup is the Background: a team standardized on the old engine, with its
// guidance composed into the profile the binding names.
func j20Setup(w *World) error {
	st := j20Of(w)
	if st.ready {
		return nil
	}
	if err := scaffoldProjectWithConfig(w, j20Config()); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/content/bundles/house.yaml",
		fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  house-guidance:\n    content: %q\nhooks:\n  session_end:\n    - command: %q\n      type: command\n",
			j20Marker, j20HookCommand)); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "house"); err != nil {
		return err
	}
	st.ready = true
	return nil
}

// j20Materialize writes the composed context for one engine's native surface
// and returns the bytes of the file that engine will actually read.
func j20Materialize(w *World, engine string) (string, error) {
	target := "out-" + engine
	if err := runOK(w, "profile", "materialize", "default", "--target", target, "--backend", engine); err != nil {
		return "", err
	}
	// Each engine reads its own filename; the whole point of P1 is that the
	// same composed context lands in each engine's own idiom.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		if body, err := w.env.ReadFile(filepath.Join(target, name)); err == nil {
			w.docStepMaterialized = body
			return body, nil
		}
	}
	return "", fmt.Errorf("materializing for %s wrote neither AGENTS.md nor CLAUDE.md into %s; ctxloom reported:\n%s",
		engine, target, w.env.LastOutput())
}

// j20ConfigSaysEngine reads the engine actually recorded for the agent in
// config.yaml — the on-disk payload, not the CLI's own success line.
func j20ConfigSaysEngine(w *World) (string, error) {
	body, err := w.env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		return "", fmt.Errorf("read config.yaml: %w", err)
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != j20Agent+":" {
			continue
		}
		for _, sub := range lines[i+1:] {
			trimmed := strings.TrimSpace(sub)
			if strings.HasPrefix(trimmed, "engine:") {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "engine:")), nil
			}
			// Stop at the next agent block.
			if trimmed != "" && !strings.HasPrefix(sub, "    ") {
				break
			}
		}
	}
	return "", fmt.Errorf("config.yaml records no engine for agent %q; it holds:\n%s", j20Agent, body)
}

func registerJ20Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice's team has standardised on one engine and is moving to another$`, func(c context.Context) error {
		return j20Setup(worldFrom(c))
	})

	ctx.Step(`^the team's guidance reaches the old engine's own surface$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j20Materialize(w, j20OldEngine)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j20Marker) {
			return fmt.Errorf("the %s surface does not carry the team's guidance before the switch, so this journey has no "+
				"before-state to compare against; it holds:\n%s", j20OldEngine, body)
		}
		j20Of(w).before = body
		return nil
	})

	ctx.Step(`^Alice swaps the engine under the binding$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("agent", "edit", j20Agent, "--engine", j20NewEngine)
		return nil
	})

	ctx.Step(`^the binding on disk names the new engine, and so does what ctxloom reports$`, func(c context.Context) error {
		w := worldFrom(c)
		got, err := j20ConfigSaysEngine(w)
		if err != nil {
			return err
		}
		if got != j20NewEngine {
			return fmt.Errorf("config.yaml still binds agent %q to %q after the swap (ctxloom reported exit %d:\n%s)",
				j20Agent, got, w.env.LastExitCode(), w.env.LastOutput())
		}
		if err := runOK(w, "agent", "show", j20Agent); err != nil {
			return err
		}
		if !strings.Contains(w.env.LastOutput(), j20NewEngine) {
			return fmt.Errorf("`agent show %s` does not name %s after the swap — the file changed and the inspector did not; "+
				"it printed:\n%s", j20Agent, j20NewEngine, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the same guidance reaches the new engine's own surface, unchanged$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j20Of(w)
		body, err := j20Materialize(w, j20NewEngine)
		if err != nil {
			return err
		}
		st.after = body
		if !strings.Contains(body, j20Marker) {
			return fmt.Errorf("the %s surface does not carry the team's guidance after the switch. The portability claim is that "+
				"profiles are engine-neutral; a migration that silently drops content is the failure that claim exists to rule "+
				"out. It holds:\n%s", j20NewEngine, body)
		}
		if st.before == "" {
			return fmt.Errorf("no before-state was captured, so 'unchanged' measured nothing")
		}
		if strings.TrimSpace(st.before) != strings.TrimSpace(st.after) {
			return fmt.Errorf("the composed context DIFFERS across the switch. Engine substitution promises the same context in a "+
				"different idiom; a difference here means the team's assistant knows something different on Monday and nobody "+
				"was told what.\n--- before (%s) ---\n%s\n--- after (%s) ---\n%s",
				j20OldEngine, st.before, j20NewEngine, st.after)
		}
		return nil
	})

	ctx.Step(`^ctxloom refuses and names the engines it knows$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("ctxloom accepted an engine name that does not exist and exited 0. On engine switch day a typo in "+
				"the engine name is the single most likely mistake anyone will make, and it is the one this command cannot "+
				"catch. Output:\n%s", w.env.LastOutput())
		}
		if !strings.Contains(w.env.LastOutput(), j20NewEngine) && !strings.Contains(w.env.LastOutput(), j20OldEngine) {
			return fmt.Errorf("ctxloom refused without naming any engine it DOES know, so the user learns their spelling was "+
				"wrong but not what the right spellings are. Output:\n%s", w.env.LastOutput())
		}
		return nil
	})

	// Asserted on the LISTING ROWS, parsed out of `--format json` stdout —
	// never on the combined output. This step used to be a substring search for
	// the harp over LastOutput(), and it could not fail: when Manager.Reconcile
	// reaps an entry it warns "session <harp> dropped from the index" on
	// stderr, so the message announcing that the history was DELETED contained
	// the very string the assertion looked for. Measured: inverting
	// operations.isUnrecoverable so a distilled entry is reaped left this
	// scenario green while `session list --all` listed nothing at all.
	ctx.Step(`^the sessions recorded under the old engine are still listed after the switch$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := runOK(w, "session", "list", "--all", "--format", "json"); err != nil {
			return err
		}
		out := w.env.LastStdout()
		var rows []struct {
			Harp string `json:"harp"`
		}
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			return fmt.Errorf("`session list --all --format json` did not emit a parseable listing (%v), so there is no row to "+
				"read the surviving session out of; stdout was:\n%s", err, out)
		}
		for _, r := range rows {
			if r.Harp == j12Harp {
				return nil
			}
		}
		return fmt.Errorf("the session recorded before the switch is no longer a row in the listing (%d row(s) returned). History "+
			"outliving the engine that produced it is the entire argument for owning a canonical transcript; a switch that "+
			"orphans it removes the reason to have one. Combined output:\n%s", len(rows), w.env.LastOutput())
	})

	ctx.Step(`^a session was recorded under the old engine before the switch$`, func(c context.Context) error {
		w := worldFrom(c)
		// Reuses J12's seeding shape deliberately: the point is a session
		// recorded by a DIFFERENT engine than the one now bound, which is
		// exactly the state a migration leaves behind.
		transcriptPath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j12HarpHome(j12Harp)+"/persist/transcript.jsonl"))
		if err := j12AddIndexEntry(w, j12Harp, "pre-switch session", transcriptPath); err != nil {
			return err
		}
		return j12WriteCanonicalTranscript(w, j12Harp, []string{
			"what did we agree here? " + j12TranscriptMarker,
			"this, for these reasons. " + j12TranscriptMarker,
		})
	})

	ctx.Step(`^Alice asks ctxloom what the switch changed and what to verify$`, func(c context.Context) error {
		w := worldFrom(c)
		// No migration surface is claimed to exist. Probe the inspectors that
		// would plausibly own the answer; the Then reports all of them.
		j19Probe(w, "doctor")
		j19Probe(w, "manage", "status")
		j19Probe(w, "agent", "show", j20Agent)
		return nil
	})

	ctx.Step(`^ctxloom names what the new engine cannot do that the old one could$`, func(c context.Context) error {
		// The concrete, verified loss on this exact pair: codex has no
		// session_end hook, so a team moving claude-code -> codex loses a hook
		// event they may well depend on, and nothing anywhere says so.
		return j19ProbesAnswered(worldFrom(c), "session_end")
	})
}

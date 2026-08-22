//go:build acceptance

// J002000: "engine switch day" (j002000_engine_switch.feature) — FLOWS-UNIFIED.md's
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
// SEAM WITH J000400. J000400 (j000400_multi_engine.feature) is the axis's REFERENCE feature:
// one profile, many engines, a matrix row per engine. It is deliberately NOT a
// journey and this file does not turn it into one. J002000 asserts the things a
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

	"github.com/ctxloom/ctxloom/internal/config"
)

const (
	// j002000Marker is the team's guidance — one string, asserted byte-identical
	// on both sides of the switch. Sameness is the whole claim: an engine
	// swap that delivers DIFFERENT-but-valid content is a migration that
	// silently changed what the assistant knows.
	j002000Marker = "J002000-TEAM-GUIDANCE-MARKER"

	j002000OldEngine = "claude-code"
	j002000NewEngine = "codex"

	// j002000Agent is the binding the whole team runs through — the thing the
	// switch is performed ON.
	j002000Agent = "dev"

	// j002000HookCommand is the team's session_end hook — real config the switch
	// actually costs something against. claude-code (the old engine) carries
	// session_end; codex (the new one) has no native session-end event
	// (codex.NoSessionEndReason), so this is what "what did the team just
	// give up" measures concretely.
	j002000HookCommand = "echo j002000-session-end"
)

// j002000State is this journey's fixture state.
type j002000State struct {
	ready bool
	// before/after hold the composed context materialized on each side of the
	// switch, so the comparison is between two real materializations rather
	// than between one and an expectation.
	before string
	after  string
}

func j002000Of(w *World) *j002000State {
	if w.j002000 == nil {
		w.j002000 = &j002000State{}
	}
	return w.j002000
}

// j002000Config renders a project declaring BOTH engines up front. A migration is
// a swap between two configured engines; a fixture with only the destination
// configured would be testing initial setup instead.
func j002000Config() string {
	var b strings.Builder
	fmt.Fprintf(&b, "version: %d\n", config.CurrentConfigVersion)
	b.WriteString("llm:\n  configs:\n")
	fmt.Fprintf(&b, "    %s:\n      type: %s\n", j002000OldEngine, j002000OldEngine)
	fmt.Fprintf(&b, "    %s:\n      type: %s\n", j002000NewEngine, j002000NewEngine)
	fmt.Fprintf(&b, "  defaults:\n    primary: %s\n    fast: %s\n", j002000OldEngine, j002000OldEngine)
	fmt.Fprintf(&b, "agents:\n  %s:\n    llm: %s\n    profiles:\n      - default\n", j002000Agent, j002000OldEngine)
	fmt.Fprintf(&b, "default_agent: %s\n", j002000Agent)
	return b.String()
}

// j002000Setup is the Background: a team standardized on the old engine, with its
// guidance composed into the profile the binding names.
func j002000Setup(w *World) error {
	st := j002000Of(w)
	if st.ready {
		return nil
	}
	if err := scaffoldProjectWithConfig(w, j002000Config()); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/content/bundles/house.yaml",
		fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  house-guidance:\n    content: %q\nhooks:\n  session_end:\n    - command: %q\n      type: command\n",
			j002000Marker, j002000HookCommand)); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "house"); err != nil {
		return err
	}
	st.ready = true
	return nil
}

// j002000Materialize writes the composed context for one engine's native surface
// and returns the bytes of the file that engine will actually read.
func j002000Materialize(w *World, engine string) (string, error) {
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

// j002000ConfigSaysEngine reads the llm label actually recorded for the agent in
// config.yaml — the on-disk payload, not the CLI's own success line.
func j002000ConfigSaysEngine(w *World) (string, error) {
	body, err := w.env.ReadFile(".ctxloom/config.yaml")
	if err != nil {
		return "", fmt.Errorf("read config.yaml: %w", err)
	}
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != j002000Agent+":" {
			continue
		}
		for _, sub := range lines[i+1:] {
			trimmed := strings.TrimSpace(sub)
			if strings.HasPrefix(trimmed, "llm:") {
				return strings.TrimSpace(strings.TrimPrefix(trimmed, "llm:")), nil
			}
			// Stop at the next agent block.
			if trimmed != "" && !strings.HasPrefix(sub, "    ") {
				break
			}
		}
	}
	return "", fmt.Errorf("config.yaml records no llm label for agent %q; it holds:\n%s", j002000Agent, body)
}

func registerJ002000Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Alice's team has standardised on one engine and is moving to another$`, func(c context.Context) error {
		return j002000Setup(worldFrom(c))
	})

	ctx.Step(`^the team's guidance reaches the old engine's own surface$`, func(c context.Context) error {
		w := worldFrom(c)
		body, err := j002000Materialize(w, j002000OldEngine)
		if err != nil {
			return err
		}
		if !strings.Contains(body, j002000Marker) {
			return fmt.Errorf("the %s surface does not carry the team's guidance before the switch, so this journey has no "+
				"before-state to compare against; it holds:\n%s", j002000OldEngine, body)
		}
		j002000Of(w).before = body
		return nil
	})

	ctx.Step(`^Alice swaps the engine under the binding$`, func(c context.Context) error {
		w := worldFrom(c)
		_ = w.env.Run("agent", "edit", j002000Agent, "--llm", j002000NewEngine)
		return nil
	})

	ctx.Step(`^the binding on disk names the new engine, and so does what ctxloom reports$`, func(c context.Context) error {
		w := worldFrom(c)
		got, err := j002000ConfigSaysEngine(w)
		if err != nil {
			return err
		}
		if got != j002000NewEngine {
			return fmt.Errorf("config.yaml still binds agent %q to %q after the swap (ctxloom reported exit %d:\n%s)",
				j002000Agent, got, w.env.LastExitCode(), w.env.LastOutput())
		}
		if err := runOK(w, "agent", "show", j002000Agent); err != nil {
			return err
		}
		if !strings.Contains(w.env.LastOutput(), j002000NewEngine) {
			return fmt.Errorf("`agent show %s` does not name %s after the swap — the file changed and the inspector did not; "+
				"it printed:\n%s", j002000Agent, j002000NewEngine, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the same guidance reaches the new engine's own surface, unchanged$`, func(c context.Context) error {
		w := worldFrom(c)
		st := j002000Of(w)
		body, err := j002000Materialize(w, j002000NewEngine)
		if err != nil {
			return err
		}
		st.after = body
		if !strings.Contains(body, j002000Marker) {
			return fmt.Errorf("the %s surface does not carry the team's guidance after the switch. The portability claim is that "+
				"profiles are engine-neutral; a migration that silently drops content is the failure that claim exists to rule "+
				"out. It holds:\n%s", j002000NewEngine, body)
		}
		if st.before == "" {
			return fmt.Errorf("no before-state was captured, so 'unchanged' measured nothing")
		}
		if strings.TrimSpace(st.before) != strings.TrimSpace(st.after) {
			return fmt.Errorf("the composed context DIFFERS across the switch. Engine substitution promises the same context in a "+
				"different idiom; a difference here means the team's assistant knows something different on Monday and nobody "+
				"was told what.\n--- before (%s) ---\n%s\n--- after (%s) ---\n%s",
				j002000OldEngine, st.before, j002000NewEngine, st.after)
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
		if !strings.Contains(w.env.LastOutput(), j002000NewEngine) && !strings.Contains(w.env.LastOutput(), j002000OldEngine) {
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
			if r.Harp == j001200Harp {
				return nil
			}
		}
		return fmt.Errorf("the session recorded before the switch is no longer a row in the listing (%d row(s) returned). History "+
			"outliving the engine that produced it is the entire argument for owning a canonical transcript; a switch that "+
			"orphans it removes the reason to have one. Combined output:\n%s", len(rows), w.env.LastOutput())
	})

	ctx.Step(`^a session was recorded under the old engine before the switch$`, func(c context.Context) error {
		w := worldFrom(c)
		// Reuses J001200's seeding shape deliberately: the point is a session
		// recorded by a DIFFERENT engine than the one now bound, which is
		// exactly the state a migration leaves behind.
		transcriptPath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200Harp)+"/persist/transcript.jsonl"))
		if err := j001200AddIndexEntry(w, j001200Harp, "pre-switch session", transcriptPath); err != nil {
			return err
		}
		return j001200WriteCanonicalTranscript(w, j001200Harp, []string{
			"what did we agree here? " + j001200TranscriptMarker,
			"this, for these reasons. " + j001200TranscriptMarker,
		})
	})

	ctx.Step(`^Alice asks ctxloom what the switch changed and what to verify$`, func(c context.Context) error {
		w := worldFrom(c)
		// No migration surface is claimed to exist. Probe the inspectors that
		// would plausibly own the answer; the Then reports all of them.
		j001900Probe(w, "doctor")
		j001900Probe(w, "manage", "check")
		j001900Probe(w, "agent", "show", j002000Agent)
		return nil
	})

	ctx.Step(`^ctxloom names what the new engine cannot do that the old one could$`, func(c context.Context) error {
		// The concrete, verified loss on this exact pair: codex has no
		// session_end hook, so a team moving claude-code -> codex loses a hook
		// event they may well depend on.
		//
		// EVERY probed surface must say so, not merely one of them. The any-of
		// form this used to take let the row go green on `agent show` alone
		// while `doctor` and `manage check` — the two surfaces above, and the
		// two a user actually reaches for — still said nothing
		// (trusting-ambiguity). A step whose prose is "nothing tells her"
		// cannot be satisfied by one of three telling her.
		return j001900EveryProbeAnswered(worldFrom(c), "session_end")
	})
}

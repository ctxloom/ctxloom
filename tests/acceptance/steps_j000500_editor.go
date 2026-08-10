//go:build acceptance

// J000500: "Dana in her editor" (j000500_editor.feature) — FLOWS-UNIFIED.md's U10.
//
// THE HONEST LIMIT OF THIS FILE, stated first because it governs what is here
// and what deliberately is not. Most of Dana's journey needs a LIVE EDITOR
// speaking ACP over stdio to a long-running `ctxloom acp serve` process. That
// is not hermetically assertable in this harness, and pretending otherwise
// would produce scenarios that pass while proving nothing about whether Zed
// can actually drive ctxloom. So the rows that need a real editor are recorded
// as unverifiable HERE rather than faked, and the live verify stays open and
// manual.
//
// What IS hermetically assertable, and what this file therefore asserts, is
// the CONFIGURATION EDGE — the part Dana touches before any editor is
// involved, and the part where the surface's problems actually live:
//   - `acp list` must emit agent blocks that are genuinely pasteable, which
//     means real JSON naming a real binary, not a prose description of one;
//   - a flag a command ACCEPTS and then DISCARDS is worse than one it rejects,
//     because the user's intent silently evaporates (task broken-sage);
//   - the differentiator's hole: an agent onboarded as bare ACP config
//     inherits none of the materialized context, hooks or history that make
//     ctxloom worth using, and nothing says so.
//
// SEAM WITH EXISTING COVERAGE. The ACP mechanics have partial coverage inside
// noun and contract features already. Nothing here re-drives the protocol
// itself; every scenario is about what Dana can see and paste.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cucumber/godog"
)

// j000500State is this journey's fixture state.
type j000500State struct {
	ready bool
}

func j000500Of(w *World) *j000500State {
	if w.j000500 == nil {
		w.j000500 = &j000500State{}
	}
	return w.j000500
}

const (
	// j000500Marker is the context Dana's binding composes — the thing that must
	// still reach her when she works through an editor instead of a terminal.
	j000500Marker = "J000500-EDITOR-BINDING-MARKER"
	// j000500Agent is the agent Zed would be pointed at.
	j000500Agent = "dev"
)

// j000500Setup is the Background: a project with an agent binding carrying real
// composed context, which is what any door into ctxloom is supposed to deliver.
func j000500Setup(w *World) error {
	st := j000500Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "claude-code", "claude-code"); err != nil {
		return err
	}
	if err := w.env.WriteFile(".ctxloom/content/bundles/house.yaml",
		fmt.Sprintf("version: \"1.0.0\"\nfragments:\n  editor-guidance:\n    content: %q\n", j000500Marker)); err != nil {
		return err
	}
	if err := runOK(w, "profile", "modify", "default", "--add-bundle", "house"); err != nil {
		return err
	}
	if err := runOK(w, "agent", "create", j000500Agent, "--llm", "claude-code", "--profiles", "default"); err != nil {
		return err
	}
	st.ready = true
	return nil
}

func registerJ000500Steps(ctx *godog.ScenarioContext) {
	ctx.Step(`^Dana lives in her editor and will not adopt a terminal workflow$`, func(c context.Context) error {
		return j000500Setup(worldFrom(c))
	})

	ctx.Step(`^the listing is something she can actually paste into her editor's config$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("`acp list` printed nothing (exit %d). Its entire job is to hand a human a block to paste; "+
				"zero bytes is the silent no-op wearing a helpful name", w.env.LastExitCode())
		}
		// "Pasteable" is a payload property, not a formatting opinion: the
		// block has to name the command an editor will execute, and it has to
		// parse as the JSON an editor config is written in. A prose listing
		// that describes the agent is a different artifact with the same words
		// in it.
		if !strings.Contains(out, "ctxloom") || !strings.Contains(out, "acp") {
			return fmt.Errorf("the listing never names the command an editor would run (`ctxloom acp serve ...`), so there is "+
				"nothing in it to paste. It printed:\n%s", out)
		}
		start := strings.Index(out, "{")
		end := strings.LastIndex(out, "}")
		if start < 0 || end <= start {
			return fmt.Errorf("the listing contains no JSON object at all, so it cannot be pasted into an editor's config "+
				"file as-is. It printed:\n%s", out)
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(out[start:end+1]), &probe); err != nil {
			return fmt.Errorf("the listing's block does not parse as JSON (%v), so pasting it produces a broken editor "+
				"config. It printed:\n%s", err, out)
		}
		return nil
	})

	ctx.Step(`^the listing names the agent she would bind to$`, func(c context.Context) error {
		w := worldFrom(c)
		if !strings.Contains(w.env.LastOutput(), j000500Agent) {
			return fmt.Errorf("`acp list` does not name the configured agent %q, so Dana cannot tell which block belongs to "+
				"which binding. It printed:\n%s", j000500Agent, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^ctxloom refuses rather than accepting flags it will discard$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		// FILED (task broken-sage): the bare `ctxloom acp` parent registers
		// --agent/--llm/--profile/--workspace and then does nothing with them.
		// A flag that is REJECTED teaches the user their command was wrong; a
		// flag that is ACCEPTED and ignored teaches them it was right. Dana
		// binds an agent, sees exit 0, and gets a session with none of that
		// agent's context — with no signal anywhere that her intent was
		// dropped.
		if w.env.LastExitCode() == 0 {
			return fmt.Errorf("bare `ctxloom acp` ACCEPTED --agent and exited 0 without serving anything. The flag is "+
				"registered on the parent command and silently discarded, so the user's binding evaporates while the command "+
				"reports success. Rejecting the invocation (or honouring the flag) both teach the truth; this teaches the "+
				"opposite. Output:\n%s", out)
		}
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("bare `ctxloom acp` failed with no message at all, so the user is told nothing about why")
		}
		return nil
	})

	ctx.Step(`^ctxloom says what a bare ACP agent will not inherit$`, func(c context.Context) error {
		w := worldFrom(c)
		// The differentiator's hole, stated as the thing a user must be told:
		// a generic-acp agent onboarded as configuration gets no materialized
		// native surface (P1 is ABSENT by design for it), no hooks, and no
		// history — that is, none of the three things that distinguish ctxloom
		// from pointing the editor straight at the vendor.
		out := w.env.LastOutput()
		var missing []string
		for _, want := range []string{"hook", "history"} {
			if !strings.Contains(strings.ToLower(out), want) {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("nothing warned Dana that a generic ACP agent inherits no %v (exit %d). For a generic-acp "+
				"binding P1 materialization is ABSENT BY DESIGN, hooks do not attach, and no transcript is captured — so the "+
				"editor door that looks equivalent to the terminal one delivers none of the three things that make ctxloom "+
				"worth the setup, and says nothing. Output:\n%s", missing, w.env.LastExitCode(), out)
		}
		return nil
	})
}

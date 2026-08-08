//go:build acceptance

// J12: "the archaeologist" (j12_recall.feature) — FLOWS-UNIFIED.md's U9.
//
// THIS JOURNEY OWNS RECALL, NOT CAPTURE. J10 (j10_transcript_capture.feature)
// already proves the import side thoroughly — four vendors' private stores
// converging into the canonical transcript, idempotent re-backfill, live
// capture left untouched. Nothing here re-drives `session backfill`. What is
// unproven, and what this journey is for, is the PAYOFF: having captured all
// that history, can anyone actually find a decision in it and put it back in
// front of a model?
//
// The recall surface exists — `session search`, `session show`,
// `run --session [--distill]` are all real commands. Their behaviour is what
// nobody has asserted, and two of the scenarios below are red against real
// filed defects rather than missing features.
//
// WHY THE MARKERS ARE SPLIT THREE WAYS. A session's searchable text lives in
// three different places (harp name, index summary, distilled essence) and a
// search that only ever matched one of them would look identical to a working
// one against a fixture where all three say the same thing. So each carries
// its OWN distinct marker, and each search scenario proves a specific field
// participates. The same split does the load-bearing work in the resume
// scenarios: the transcript and the essence carry different markers, so
// "resumed via the full transcript" and "resumed via the essence" are
// distinguishable outcomes rather than one assertion satisfied by either.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cucumber/godog"
)

const (
	// Three fields, three markers — see the file doc.
	j12EssenceMarker    = "J12-ESSENCE-WORKTREE-NAMING-DECISION"
	j12SummaryMarker    = "J12-SUMMARY-ONLY-MARKER"
	j12TranscriptMarker = "J12-TRANSCRIPT-ONLY-MARKER"

	// j12Harp is the March session everybody half-remembers.
	j12Harp = "amber-quiet-heron"
	// j12BareHarp is a session that was captured and NEVER distilled — the
	// B9 case, which is the default rather than the exception, since nothing
	// distills a session automatically when it ends.
	j12BareHarp = "brisk-copper-moth"
)

// j12State is this journey's fixture state.
type j12State struct {
	entries []string // accumulated index entries, so multiple harps coexist
	ready   bool
}

func j12Of(w *World) *j12State {
	if w.j12 == nil {
		w.j12 = &j12State{}
	}
	return w.j12
}

// j12AddIndexEntry appends one session to the index and rewrites it. Shaped
// like steps_j10_transcript_capture.go's j10AddIndexEntry (accumulating rather
// than overwriting) because a recall journey is inherently multi-session: a
// search that returns one hit proves nothing unless there was something else
// it could have returned instead.
func j12AddIndexEntry(w *World, harp, summary, transcriptPath string) error {
	st := j12Of(w)
	st.entries = append(st.entries, fmt.Sprintf(
		"  - harp_name: %s\n"+
			"    session_id: seeded-%s\n"+
			"    backend: mock\n"+
			"    project_dir: %s\n"+
			"    started_at: 2026-03-14T00:00:00Z\n"+
			"    ended_at: 2026-03-14T02:00:00Z\n"+
			"    transcript_path: %q\n"+
			"    summary: %s\n", harp, harp, w.env.ProjectDir, transcriptPath, summary))
	return w.env.WriteHomeFile(".ctxloom/sessions/index.yaml", "sessions:\n"+strings.Join(st.entries, ""))
}

// j12HarpHome returns a harp's directory relative to the isolated HOME.
func j12HarpHome(harp string) string { return ".ctxloom/sessions/" + harp }

// j12WriteEssence writes a harp's distilled essence — the DERIVED artifact
// `session show` prints and `--distill` resumes through.
func j12WriteEssence(w *World, harp, body string) error {
	return w.env.WriteHomeFile(j12HarpHome(harp)+"/essence.md",
		fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-03-15T00:00:00Z\n---\n\n%s\n", harp, body))
}

// j12WriteCanonicalTranscript writes a harp's canonical transcript in the real
// on-disk schema (internal/transcript.Record), at the path
// paths.ResolveHarpCanonicalTranscriptPath resolves — hand-rendered as JSON
// rather than marshalled through the production type so that a schema change
// shows up here as a deliberate fixture update instead of silently reshaping
// what this journey claims to have recorded.
func j12WriteCanonicalTranscript(w *World, harp string, turns []string) error {
	var b strings.Builder
	ts := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	for i, text := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		rec := map[string]any{
			"v":          1,
			"harp":       harp,
			"session_id": "seeded-" + harp,
			"engine":     "mock",
			"seq":        i,
			"ts":         ts.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"kind":       "entry",
			"entry":      map[string]any{"type": role, "content": text},
		}
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("render transcript line %d: %w", i, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return w.env.WriteHomeFile(j12HarpHome(harp)+"/persist/transcript.jsonl", b.String())
}

// j12Setup is the Background: a project, plus the March session as a fully
// recalled artifact — index entry, canonical transcript, distilled essence —
// and a second, undistilled session beside it so every search assertion has a
// wrong answer available to give.
func j12Setup(w *World) error {
	st := j12Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
		return err
	}

	transcriptPath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j12HarpHome(j12Harp)+"/persist/transcript.jsonl"))
	if err := j12AddIndexEntry(w, j12Harp, "March design session, "+j12SummaryMarker, transcriptPath); err != nil {
		return err
	}
	if err := j12WriteCanonicalTranscript(w, j12Harp, []string{
		"Why did we settle on that worktree layout? " + j12TranscriptMarker,
		"Because leaf names have to name the project, not the branch. " + j12TranscriptMarker,
	}); err != nil {
		return err
	}
	if err := j12WriteEssence(w, j12Harp,
		"Decision: worktrees are flat, named <project>--<branch>. "+j12EssenceMarker); err != nil {
		return err
	}

	// The undistilled neighbour: captured, never summarized. This is the
	// DEFAULT state of a session, not an edge case.
	barePath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j12HarpHome(j12BareHarp)+"/persist/transcript.jsonl"))
	if err := j12AddIndexEntry(w, j12BareHarp, "unrelated afternoon session", barePath); err != nil {
		return err
	}
	if err := j12WriteCanonicalTranscript(w, j12BareHarp, []string{"unrelated chatter", "unrelated reply"}); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j12AssertNames checks the last command's output names every want, quoting
// the whole output on failure.
func j12AssertNames(w *World, what string, wants ...string) error {
	out := w.env.LastOutput()
	var missing []string
	for _, want := range wants {
		if !strings.Contains(out, want) {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s did not name %v (exit %d); the whole output was:\n%s",
			what, missing, w.env.LastExitCode(), out)
	}
	return nil
}

func registerJ12Steps(ctx *godog.ScenarioContext) {
	// --- Background ---------------------------------------------------------

	ctx.Step(`^a design question everyone remembers deciding and nobody remembers why$`, func(c context.Context) error {
		return j12Setup(worldFrom(c))
	})

	// --- Search -------------------------------------------------------------

	ctx.Step(`^the search names the March session by a phrase that appears only in its distilled essence$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j12AssertNames(w, "`session search`", j12Harp); err != nil {
			return err
		}
		if strings.Contains(w.env.LastOutput(), j12BareHarp) {
			return fmt.Errorf("the search also returned %q, whose essence does not exist and whose text does not match — "+
				"a search that returns everything is not a search. Output:\n%s", j12BareHarp, w.env.LastOutput())
		}
		return nil
	})

	ctx.Step(`^the search names the March session by a phrase that appears only in its index summary$`, func(c context.Context) error {
		return j12AssertNames(worldFrom(c), "`session search` over summaries", j12Harp)
	})

	ctx.Step(`^ctxloom says plainly that nothing matched$`, func(c context.Context) error {
		w := worldFrom(c)
		out := strings.TrimSpace(w.env.LastOutput())
		if out == "" {
			return fmt.Errorf("`session search` answered a no-match query with ZERO BYTES and exit %d. "+
				"An archivist cannot tell 'nothing matched' from 'the search did not run' — and this codebase's "+
				"characteristic bug is exactly a successful-looking command that produced nothing", w.env.LastExitCode())
		}
		if strings.Contains(out, j12Harp) || strings.Contains(out, j12BareHarp) {
			return fmt.Errorf("a query matching nothing returned session names anyway; output:\n%s", out)
		}
		return nil
	})

	// --- Show ---------------------------------------------------------------

	ctx.Step(`^ctxloom prints the decision the session reached$`, func(c context.Context) error {
		return j12AssertNames(worldFrom(c), "`session show`", j12EssenceMarker)
	})

	ctx.Step(`^ctxloom says the session was never distilled and names how to distill it$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.env.LastExitCode() == 0 && strings.Contains(w.env.LastOutput(), j12EssenceMarker) {
			return fmt.Errorf("`session show` printed a distilled essence for a session that has none — the fixture is wrong")
		}
		// The product states this gap in its own help text ("Distillation is
		// on-demand: nothing distills a session automatically when it ends"),
		// so the error a user hits should point at the same fact and at the
		// command that fixes it. Anything less leaves them believing the
		// session was not recorded at all.
		return j12AssertNames(w, "the not-distilled error", "session distill")
	})

	// --- Resume: the payoff -------------------------------------------------

	ctx.Step(`^the assembled context carries the conversation she had in March$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		if !strings.Contains(out, j12TranscriptMarker) {
			return fmt.Errorf("`run --session %s --dry-run` assembled a context that does NOT carry the recorded conversation "+
				"(exit %d). The recall payoff is the whole point of capturing transcripts: history that can be found but not "+
				"put back in front of a model is an archive, not a memory. What it assembled was:\n%s",
				j12Harp, w.env.LastExitCode(), out)
		}
		return nil
	})

	ctx.Step(`^the assembled context carries the distilled essence and not the raw conversation$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		hasEssence := strings.Contains(out, j12EssenceMarker)
		hasRaw := strings.Contains(out, j12TranscriptMarker)
		switch {
		case !hasEssence && hasRaw:
			return fmt.Errorf("--distill resumed via the RAW transcript rather than the essence: the two modes are not "+
				"distinguishable in what reaches the model, so --distill buys nothing. Output:\n%s", out)
		case !hasEssence:
			return fmt.Errorf("--distill assembled a context carrying neither the essence nor the transcript (exit %d). "+
				"Note the distilled path rides CTXLOOM_RESUMED_FROM/PARTS and a SessionStart hook rather than the assembled "+
				"context itself — if that is the intended design, this scenario is the record that a user cannot SEE what "+
				"was resumed. Output:\n%s", w.env.LastExitCode(), out)
		case hasRaw:
			return fmt.Errorf("--distill carried the essence AND the whole raw transcript — the compression it exists to "+
				"provide did not happen. Output:\n%s", out)
		}
		return nil
	})

	ctx.Step(`^ctxloom warns and runs anyway, naming the harp it could not find$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		// FILED (task diffusive-dazzler, and reported in
		// cli.TestRunCharacterization's own FINDING comment):
		// operations.GetSession returns (nil, nil) for an absent harp and
		// operations.RecordedSessionEntries dereferences entry.SessionID
		// without checking, so the UNRESOLVABLE half of resumeFullContext's
		// stated contract never reaches its warn. The UNBOUND half degrades
		// correctly; this one panics.
		for _, tell := range []string{"panic:", "runtime error:", "invalid memory address", "nil pointer dereference"} {
			if strings.Contains(out, tell) {
				return fmt.Errorf("`run --session <unknown-harp>` PANICKED instead of degrading (%s). resumeFullContext's own "+
					"contract says an unresolvable harp warns and leaves the context unchanged; a mistyped harp name — the most "+
					"ordinary error there is on a command whose arguments are three random words — takes down the process. "+
					"Output:\n%s", tell, out)
			}
		}
		return j12AssertNames(w, "the degrade", "no-such-harp-anywhere")
	})

	// --- Retention of the raw record ---------------------------------------

	ctx.Step(`^the canonical transcript she recalled from is still on disk, untouched$`, func(c context.Context) error {
		w := worldFrom(c)
		rel := j12HarpHome(j12Harp) + "/persist/transcript.jsonl"
		body, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("the canonical transcript at %s is gone after a recall: %w. Recall is a READ — "+
				"a resume that consumes the record it read is a memory that erases itself on use", rel, err)
		}
		if !strings.Contains(body, j12TranscriptMarker) {
			return fmt.Errorf("the canonical transcript no longer carries its own recorded turns; it holds:\n%s", body)
		}
		if _, err := os.Stat(filepath.Join(w.env.HomeDir, filepath.FromSlash(j12HarpHome(j12Harp)+"/essence.md"))); err != nil {
			return fmt.Errorf("the distilled essence is gone after a recall: %w", err)
		}
		return nil
	})
}

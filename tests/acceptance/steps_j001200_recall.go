//go:build acceptance

// J001200: "the archaeologist" (j001200_recall.feature) — FLOWS-UNIFIED.md's U9.
//
// THIS JOURNEY OWNS RECALL, NOT CAPTURE. J001000 (j001000_transcript_capture.feature)
// already proves the import side thoroughly — four vendors' private stores
// converging into the canonical transcript, idempotent re-backfill, live
// capture left untouched. Nothing here re-drives the transcript import. What is
// unproven, and what this journey is for, is the PAYOFF: having captured all
// that history, can anyone actually find a decision in it and put it back in
// front of a model?
//
// The recall surface: `session search`, `session show` and `run --session`.
// A session is titled by its first user prompt (read from its transcript),
// so the title and the rest of the conversation carry different markers —
// "found by its title" and "resumed with its conversation" are
// distinguishable outcomes rather than one assertion either would satisfy.
package acceptance

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/engines/mock"

	"github.com/cucumber/godog"
)

const (
	// Two markers — see the file doc.
	j001200TitleMarker      = "J001200-TITLE-WORKTREE-QUESTION"
	j001200TranscriptMarker = "J001200-TRANSCRIPT-ONLY-MARKER"

	// j001200Harp is the March session everybody half-remembers.
	j001200Harp = "amber-quiet-heron"
	// j001200BareHarp is an unrelated session beside it, so every search has
	// a wrong answer available to give.
	j001200BareHarp = "brisk-copper-moth"
)

// j001200State is this journey's fixture state.
type j001200State struct {
	ready bool
}

func j001200Of(w *World) *j001200State {
	if w.j001200 == nil {
		w.j001200 = &j001200State{}
	}
	return w.j001200
}

// j001200AddIndexEntry records one ended session for harp bound to
// transcriptPath. A recall journey is inherently multi-session — a search
// that returns one hit proves nothing unless there was something else it
// could have returned — and each call plants its own session directory, so
// earlier sessions survive later additions. The title a listing shows comes
// from the harp's transcript, never from this record.
func j001200AddIndexEntry(w *World, harp, transcriptPath string) error {
	return seedSessionSidecar(w, harp, sessionSeed{
		SessionID:      "seeded-" + harp,
		Backend:        string(mock.Name),
		EngineVersion:  j001000SeededEngineVersion(string(mock.Name)),
		StartedAt:      "2026-03-14T00:00:00Z",
		EndedAt:        "2026-03-14T02:00:00Z",
		TranscriptPath: transcriptPath,
	})
}

// j001200HarpHome returns a harp's directory relative to the isolated HOME.
func j001200HarpHome(harp string) string { return ".ctxloom/sessions/" + harp }

// j001200WriteCanonicalTranscript writes a harp's canonical transcript in the real
// on-disk schema (internal/adapters/transcript.Record), at the path
// paths.HarpCanonicalTranscriptPath names — hand-rendered as JSON
// rather than marshalled through the production type so that a schema change
// shows up here as a deliberate fixture update instead of silently reshaping
// what this journey claims to have recorded.
func j001200WriteCanonicalTranscript(w *World, harp string, turns []string) error {
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
	return w.env.WriteHomeFile(j001200HarpHome(harp)+"/transcripts/transcript.jsonl", b.String())
}

// j001200Setup is the Background: a project, plus the March session — index
// entry and canonical transcript — and a second, unrelated session beside it
// so every search assertion has a wrong answer available to give.
func j001200Setup(w *World) error {
	st := j001200Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
		return err
	}

	transcriptPath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200Harp)+"/transcripts/transcript.jsonl"))
	if err := j001200AddIndexEntry(w, j001200Harp, transcriptPath); err != nil {
		return err
	}
	if err := j001200WriteCanonicalTranscript(w, j001200Harp, []string{
		"Why did we settle on that worktree layout? " + j001200TitleMarker,
		"Because leaf names have to name the project, not the branch. " + j001200TranscriptMarker,
	}); err != nil {
		return err
	}

	// The unrelated neighbour.
	barePath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200BareHarp)+"/transcripts/transcript.jsonl"))
	if err := j001200AddIndexEntry(w, j001200BareHarp, barePath); err != nil {
		return err
	}
	if err := j001200WriteCanonicalTranscript(w, j001200BareHarp, []string{"unrelated chatter", "unrelated reply"}); err != nil {
		return err
	}

	st.ready = true
	return nil
}

// j001200AssertNames checks that out — the stream the caller chose — names
// every want, quoting it whole on failure. The stream is the caller's to pick
// because the two kinds of claim read different ones: a RESULT (a listing
// names the session) is stdout alone, since a stderr warning that quotes the
// same harp — Reconcile's "session <harp> dropped from the index" — would
// otherwise satisfy a search that returned nothing; a DIAGNOSTIC (the error
// names the remedy) is the combined stream, where the diagnostic lives.
func j001200AssertNames(w *World, out, what string, wants ...string) error {
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

func registerJ001200Steps(ctx *godog.ScenarioContext) {
	// --- Background ---------------------------------------------------------

	ctx.Step(`^a design question everyone remembers deciding and nobody remembers why$`, func(c context.Context) error {
		return j001200Setup(worldFrom(c))
	})

	// --- Search -------------------------------------------------------------

	ctx.Step(`^the search names the March session by a phrase from its first prompt$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001200AssertNames(w, w.env.LastStdout(), "`session search`", j001200Harp); err != nil {
			return err
		}
		if strings.Contains(w.env.LastStdout(), j001200BareHarp) {
			return fmt.Errorf("the search also returned %q, whose title does not match — "+
				"a search that returns everything is not a search. Stdout:\n%s", j001200BareHarp, w.env.LastStdout())
		}
		return nil
	})

	ctx.Step(`^ctxloom says plainly that nothing matched$`, func(c context.Context) error {
		w := worldFrom(c)
		out := strings.TrimSpace(w.env.LastStdout())
		if out == "" {
			return fmt.Errorf("`session search` answered a no-match query with ZERO BYTES on stdout and exit %d. "+
				"An archivist cannot tell 'nothing matched' from 'the search did not run' — and this codebase's "+
				"characteristic bug is exactly a successful-looking command that produced nothing", w.env.LastExitCode())
		}
		if strings.Contains(out, j001200Harp) || strings.Contains(out, j001200BareHarp) {
			return fmt.Errorf("a query matching nothing returned session names anyway; stdout:\n%s", out)
		}
		return nil
	})

	// --- Show ---------------------------------------------------------------

	ctx.Step(`^ctxloom prints the question the session was opened with$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001200AssertNames(w, w.env.LastStdout(), "`session show`", j001200TitleMarker)
	})

	// --- Resume: the payoff -------------------------------------------------

	ctx.Step(`^the assembled context carries the conversation she had in March$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastStdout()
		if !strings.Contains(out, j001200TranscriptMarker) {
			return fmt.Errorf("`run --session %s --dry-run` assembled a context that does NOT carry the recorded conversation "+
				"(exit %d). The recall payoff is the whole point of capturing transcripts: history that can be found but not "+
				"put back in front of a model is an archive, not a memory. What it assembled was:\n%s",
				j001200Harp, w.env.LastExitCode(), out)
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
		return j001200AssertNames(w, w.env.LastOutput(), "the degrade", "no-such-harp-anywhere")
	})

	// --- Retention of the raw record ---------------------------------------

	ctx.Step(`^the canonical transcript she recalled from is still on disk, untouched$`, func(c context.Context) error {
		w := worldFrom(c)
		rel := j001200HarpHome(j001200Harp) + "/transcripts/transcript.jsonl"
		body, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("the canonical transcript at %s is gone after a recall: %w. Recall is a READ — "+
				"a resume that consumes the record it read is a memory that erases itself on use", rel, err)
		}
		if !strings.Contains(body, j001200TranscriptMarker) {
			return fmt.Errorf("the canonical transcript no longer carries its own recorded turns; it holds:\n%s", body)
		}
		return nil
	})
}

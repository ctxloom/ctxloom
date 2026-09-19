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

	"github.com/ctxloom/ctxloom/internal/core/config"
)

const (
	// Three fields, three markers — see the file doc.
	j001200EssenceMarker    = "J001200-ESSENCE-WORKTREE-NAMING-DECISION"
	j001200SummaryMarker    = "J001200-SUMMARY-ONLY-MARKER"
	j001200TranscriptMarker = "J001200-TRANSCRIPT-ONLY-MARKER"

	// j001200Harp is the March session everybody half-remembers.
	j001200Harp = "amber-quiet-heron"
	// j001200BareHarp is a session that was captured and NEVER distilled — the
	// B9 case, which is the default rather than the exception, since nothing
	// distills a session automatically when it ends.
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
// earlier sessions survive later additions. The summary a listing shows
// comes from the harp's essence, never from this record.
func j001200AddIndexEntry(w *World, harp, _, transcriptPath string) error {
	return seedSessionSidecar(w, harp, sessionSeed{
		SessionID:      "seeded-" + harp,
		Backend:        config.BackendMock,
		EngineVersion:  j001000SeededEngineVersion(config.BackendMock),
		StartedAt:      "2026-03-14T00:00:00Z",
		EndedAt:        "2026-03-14T02:00:00Z",
		TranscriptPath: transcriptPath,
	})
}

// j001200HarpHome returns a harp's directory relative to the isolated HOME.
func j001200HarpHome(harp string) string { return ".ctxloom/sessions/" + harp }

// j001200WriteEssence writes a harp's distilled essence — the DERIVED artifact
// `session show` prints and `--distill` resumes through. The frontmatter
// summary is where a session's one-line summary actually lives now (the
// session store derives Entry.Summary from it), so a summary-only marker
// belongs HERE, not in the pre-migration index row.
func j001200WriteEssence(w *World, harp, summary, body string) error {
	return w.env.WriteHomeFile(j001200HarpHome(harp)+"/essence.md",
		fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-03-15T00:00:00Z\nsummary: %s\n---\n\n%s\n", harp, summary, body))
}

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
	return w.env.WriteHomeFile(j001200HarpHome(harp)+"/persist/transcript.jsonl", b.String())
}

// j001200VendorTranscriptPath is where a seeded mock session's VENDOR-native
// transcript lives — the file the index binds, and the one ctxloom converts
// into the canonical transcript beside it.
//
// It exists because mock is a REAL engine here, not a hole in the registry.
// These fixtures used to bind the CANONICAL transcript as the entry's
// transcript_path, which only worked while mock had no vendor reader: nothing
// ever tried to convert it. Once mock gained one, RefreshVendorTranscript read
// that canonical file as though it were vendor-native, recognized none of its
// lines, and replaced it with the conversion's (empty) output. Binding a real
// vendor file makes the fixture match how every engine actually works, and
// makes acceptance exercise the conversion instead of stepping around it.
func j001200VendorTranscriptPath(w *World, harp string) string {
	return w.env.HomeDir + "/" + j001200HarpHome(harp) + "/vendor/mock-session.jsonl"
}

// j001200WriteMockVendorTranscript writes turns as MOCK-format vendor JSONL
// ({"role":..,"text":..,"ts":..}), the format
// internal/adapters/transcript/vendorreader/mock parses. Same turns as
// j001200WriteCanonicalTranscript, one format earlier in the pipeline.
func j001200WriteMockVendorTranscript(w *World, harp string, turns []string) error {
	var b strings.Builder
	ts := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	for i, text := range turns {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		line, err := json.Marshal(map[string]any{
			"role": role,
			"text": text,
			"ts":   ts.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("render mock vendor line %d: %w", i, err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return w.env.WriteHomeFile(j001200HarpHome(harp)+"/vendor/mock-session.jsonl", b.String())
}

// j001200SeedTranscripts writes BOTH representations of the same turns: the
// vendor-native file the index binds, and the canonical file conversion would
// produce. Both, deliberately — a scenario that reads the canonical transcript
// without ever triggering a conversion still finds one, and a scenario that
// does convert gets equivalent content rather than a contradiction.
func j001200SeedTranscripts(w *World, harp string, turns []string) error {
	if err := j001200WriteMockVendorTranscript(w, harp, turns); err != nil {
		return err
	}
	return j001200WriteCanonicalTranscript(w, harp, turns)
}

// j001200Setup is the Background: a project, plus the March session as a fully
// recalled artifact — index entry, canonical transcript, distilled essence —
// and a second, undistilled session beside it so every search assertion has a
// wrong answer available to give.
func j001200Setup(w *World) error {
	st := j001200Of(w)
	if st.ready {
		return nil
	}
	if err := ensureProjectWithEngine(w, "mock", "mock"); err != nil {
		return err
	}

	transcriptPath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200Harp)+"/persist/transcript.jsonl"))
	if err := j001200AddIndexEntry(w, j001200Harp, "March design session, "+j001200SummaryMarker, transcriptPath); err != nil {
		return err
	}
	if err := j001200WriteCanonicalTranscript(w, j001200Harp, []string{
		"Why did we settle on that worktree layout? " + j001200TranscriptMarker,
		"Because leaf names have to name the project, not the branch. " + j001200TranscriptMarker,
	}); err != nil {
		return err
	}
	if err := j001200WriteEssence(w, j001200Harp,
		"March design session, "+j001200SummaryMarker,
		"Decision: worktrees are flat, named <project>--<branch>. "+j001200EssenceMarker); err != nil {
		return err
	}

	// The undistilled neighbour: captured, never summarized. This is the
	// DEFAULT state of a session, not an edge case.
	barePath := filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200BareHarp)+"/persist/transcript.jsonl"))
	if err := j001200AddIndexEntry(w, j001200BareHarp, "unrelated afternoon session", barePath); err != nil {
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

	ctx.Step(`^the search names the March session by a phrase that appears only in its distilled essence$`, func(c context.Context) error {
		w := worldFrom(c)
		if err := j001200AssertNames(w, w.env.LastStdout(), "`session search`", j001200Harp); err != nil {
			return err
		}
		if strings.Contains(w.env.LastStdout(), j001200BareHarp) {
			return fmt.Errorf("the search also returned %q, whose essence does not exist and whose text does not match — "+
				"a search that returns everything is not a search. Stdout:\n%s", j001200BareHarp, w.env.LastStdout())
		}
		return nil
	})

	ctx.Step(`^the search names the March session by a phrase that appears only in its index summary$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001200AssertNames(w, w.env.LastStdout(), "`session search` over summaries", j001200Harp)
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

	ctx.Step(`^ctxloom prints the decision the session reached$`, func(c context.Context) error {
		w := worldFrom(c)
		return j001200AssertNames(w, w.env.LastStdout(), "`session show`", j001200EssenceMarker)
	})

	ctx.Step(`^ctxloom says the session was never distilled and names how to distill it$`, func(c context.Context) error {
		w := worldFrom(c)
		if w.env.LastExitCode() == 0 && strings.Contains(w.env.LastOutput(), j001200EssenceMarker) {
			return fmt.Errorf("`session show` printed a distilled essence for a session that has none — the fixture is wrong")
		}
		// The product states this gap in its own help text ("Distillation is
		// on-demand: nothing distills a session automatically when it ends"),
		// so the error a user hits should point at the same fact and at the
		// command that fixes it. Anything less leaves them believing the
		// session was not recorded at all.
		return j001200AssertNames(w, w.env.LastOutput(), "the not-distilled error", "session distill")
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

	ctx.Step(`^the assembled context carries the distilled essence and not the raw conversation$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastOutput()
		hasEssence := strings.Contains(out, j001200EssenceMarker)
		hasRaw := strings.Contains(out, j001200TranscriptMarker)
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
		return j001200AssertNames(w, w.env.LastOutput(), "the degrade", "no-such-harp-anywhere")
	})

	// --- Retention of the raw record ---------------------------------------

	ctx.Step(`^the canonical transcript she recalled from is still on disk, untouched$`, func(c context.Context) error {
		w := worldFrom(c)
		rel := j001200HarpHome(j001200Harp) + "/persist/transcript.jsonl"
		body, err := w.env.ReadHomeFile(rel)
		if err != nil {
			return fmt.Errorf("the canonical transcript at %s is gone after a recall: %w. Recall is a READ — "+
				"a resume that consumes the record it read is a memory that erases itself on use", rel, err)
		}
		if !strings.Contains(body, j001200TranscriptMarker) {
			return fmt.Errorf("the canonical transcript no longer carries its own recorded turns; it holds:\n%s", body)
		}
		if _, err := os.Stat(filepath.Join(w.env.HomeDir, filepath.FromSlash(j001200HarpHome(j001200Harp)+"/essence.md"))); err != nil {
			return fmt.Errorf("the distilled essence is gone after a recall: %w", err)
		}
		return nil
	})
}

//go:build acceptance

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

// Fixtures for the per-noun `session` CLI spec (features/cli/session.feature).
//
// The shared "a recorded session" step seeds an index entry and a transcript,
// which is all a READING scenario needs. Every destroyer needs two more
// things: an ended_at (each one refuses a session that may still be writing)
// and an end far enough back that transcript retention releases it — the
// default session_transcript_retention is 30 days, so a "finished" session
// here ended in January and has not been touched since.
func registerCLISessionSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a finished session "([^"]*)" with a transcript$`, func(c context.Context, harp string) error {
		return seedFinishedSession(c, harp, finishedLongAgo, false)
	})

	// The file an older ctxloom's compaction left in the output dir: every
	// destroyer must leave it where it is.
	ctx.Step(`^a finished session "([^"]*)" with a transcript and a summary an older ctxloom left$`, func(c context.Context, harp string) error {
		return seedFinishedSession(c, harp, finishedLongAgo, true)
	})

	// Within transcript retention: the transcript is the session's record.
	ctx.Step(`^a session "([^"]*)" with a transcript that finished an hour ago$`, func(c context.Context, harp string) error {
		return seedFinishedSession(c, harp, time.Now().Add(-time.Hour).UTC(), false)
	})

	// The two states the session lock cannot turn into permission. A session
	// from before the lock existed has no lock file at all; a session resumed
	// under its harp holds the lock again while ended_at still reads as set.
	ctx.Step(`^a finished session "([^"]*)" with a transcript but no session lock$`, func(c context.Context, harp string) error {
		return seedFinishedSessionFiles(c, harp, finishedLongAgo, false)
	})
	ctx.Step(`^a session "([^"]*)" with a transcript that is running again$`, func(c context.Context, harp string) error {
		if err := seedFinishedSessionFiles(c, harp, finishedLongAgo, false); err != nil {
			return err
		}
		return seedLiveSession(worldFrom(c), harp)
	})

	// The negative counterpart of "the home file X exists". Every destructive
	// scenario asserts both directions — the report side that the file is
	// still there, the apply side that it is gone — and only one of the two
	// existed.
	ctx.Step(`^the home file "([^"]*)" does not exist$`, func(c context.Context, rel string) error {
		w := worldFrom(c)
		if w.env.HomeFileExists(rel) {
			return fmt.Errorf("home file %q unexpectedly still exists", rel)
		}
		return nil
	})

	// The output-dir pair: a readable output (a plan, an older summary) lives in
	// the directory the harp's record names, not under the ctxloom home, so
	// these resolve that record rather than take a home-relative path.
	ctx.Step(`^the output file "([^"]*)" of session "([^"]*)" exists$`, func(c context.Context, name, harp string) error {
		p := filepath.Join(outputDirFor(worldFrom(c), harp), filepath.FromSlash(name))
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("output file %s of %s: %w", name, harp, err)
		}
		return nil
	})
	ctx.Step(`^the output file "([^"]*)" of session "([^"]*)" does not exist$`, func(c context.Context, name, harp string) error {
		p := filepath.Join(outputDirFor(worldFrom(c), harp), filepath.FromSlash(name))
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("output file %s of %s unexpectedly still exists", name, harp)
		}
		return nil
	})

	// Fixtures for `session adopt` (features/cli/session.feature's "Adopt"
	// rule): a harp bound to a real claude-shaped vendor transcript, an extra
	// vendor file sitting in that SAME directory the index never recorded
	// (the orphan `session adopt` exists to find), and a bare backend-only
	// seed for the unsupported-backend refusal — none of which need an
	// ended_at or an output, since adopt reads the vendor store directly
	// rather than any destroyer's on-disk artifacts.
	ctx.Step(`^a recorded session "([^"]*)" with a claude vendor transcript spanning "([^"]*)" to "([^"]*)"$`, func(c context.Context, harp, start, end string) error {
		return seedAdoptHarp(c, harp, "claude-code", harp+"-live", start, end)
	})

	ctx.Step(`^an orphaned claude vendor transcript "([^"]*)" for "([^"]*)" spanning "([^"]*)" to "([^"]*)"$`, func(c context.Context, candidateID, _, start, end string) error {
		return writeAdoptVendorHomeFile(c, candidateID, start, end)
	})

	// NDJSON, what a pipe into jq reads: stdout is one or more lines, each
	// one JSON object and nothing else.
	ctx.Step(`^every line of stdout is a JSON object$`, func(c context.Context) error {
		w := worldFrom(c)
		out := w.env.LastStdout()
		if strings.TrimSpace(out) == "" {
			return fmt.Errorf("stdout is empty")
		}
		for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				return fmt.Errorf("stdout line %q is not a JSON object: %w; stdout:\n%s", line, err, out)
			}
		}
		return nil
	})

	ctx.Step(`^a recorded session "([^"]*)" for backend "([^"]*)"$`, func(c context.Context, harp, backend string) error {
		w := worldFrom(c)
		return seedSessionSidecar(w, harp, sessionSeed{
			SessionID:      harp + "-live",
			Backend:        backend,
			StartedAt:      "2026-01-01T00:00:00Z",
			TranscriptPath: filepath.Join(w.env.HomeDir, adoptVendorDirRel, harp+"-live.jsonl"),
		})
	})
}

// adoptVendorDirRel is the ONE directory every `session adopt` fixture in
// this file writes vendor transcripts under, home-relative — every scenario
// seeds a single harp, so a shared, fixed directory (rather than one derived
// per harp) is enough for "an orphaned vendor transcript" to land in the
// SAME directory `session adopt` scans (filepath.Dir of the harp's own
// transcript_path), without a step having to look that path back up.
const adoptVendorDirRel = "claude-projects-fixture"

// writeAdoptVendorHomeFile writes a minimal claude-code-shaped vendor
// transcript at adoptVendorDirRel/<sessionID>.jsonl carrying two lines
// timestamped start and end — the RFC3339 span `session adopt`'s ordering
// rule reads (mtime is deliberately never touched: the ordering rule this
// fixture exercises is that mtime is never consulted at all).
func writeAdoptVendorHomeFile(c context.Context, sessionID, start, end string) error {
	w := worldFrom(c)
	line := func(ts string) string {
		return fmt.Sprintf(`{"type":"user","sessionId":%q,"timestamp":%q}`, sessionID, ts)
	}
	content := line(start) + "\n" + line(end) + "\n"
	return w.env.WriteHomeFile(filepath.Join(adoptVendorDirRel, sessionID+".jsonl"), content)
}

// seedAdoptHarp records a session bound to a real vendor transcript
// (sessionID.jsonl under adoptVendorDirRel) spanning start to end, so
// `session adopt` has a live binding to scan alongside and a real directory
// to scan for orphans.
func seedAdoptHarp(c context.Context, harp, backend, sessionID, start, end string) error {
	w := worldFrom(c)
	if err := seedSessionSidecar(w, harp, sessionSeed{
		SessionID:      sessionID,
		Backend:        backend,
		StartedAt:      "2026-01-01T00:00:00Z",
		TranscriptPath: filepath.Join(w.env.HomeDir, adoptVendorDirRel, sessionID+".jsonl"),
	}); err != nil {
		return err
	}
	return writeAdoptVendorHomeFile(c, sessionID, start, end)
}

// finishedLongAgo is when a "finished" fixture session ended: far enough
// back that the default session_transcript_retention (30d) releases it.
var finishedLongAgo = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

// seedFinishedSession records a session that ENDED at ended, with a real
// canonical transcript, and (leftover) the summary an older ctxloom left in
// its output dir, then proves it finished with a FREE lock file — the one
// state that lets a destroyer act.
func seedFinishedSession(c context.Context, harp string, ended time.Time, leftover bool) error {
	if err := seedFinishedSessionFiles(c, harp, ended, leftover); err != nil {
		return err
	}
	return seedDeadSession(worldFrom(c), harp)
}

// seedFinishedSessionFiles is seedFinishedSession's record-and-files half,
// with no word about liveness: the caller decides whether the harp gets a
// free lock, a held one, or none. The session directory is backdated to the
// end, so retention reads the session as untouched since it ended.
func seedFinishedSessionFiles(c context.Context, harp string, ended time.Time, leftover bool) error {
	w := worldFrom(c)
	if err := seedSessionSidecar(w, harp, sessionSeed{
		SessionID:      "seeded-" + harp,
		Backend:        "claude-code",
		StartedAt:      ended.Add(-24 * time.Hour).Format(time.RFC3339),
		EndedAt:        ended.Format(time.RFC3339),
		TranscriptPath: filepath.Join(w.env.HomeDir, seededTranscriptRel(harp)),
	}); err != nil {
		return err
	}
	if err := writeSeededTranscript(w, harp); err != nil {
		return err
	}
	if leftover {
		if err := writeOutputFile(w, harp, leftoverSummaryName, "# Session summary\n\nWritten by an older ctxloom.\n"); err != nil {
			return err
		}
	}
	return backdateSessionDir(w, harp, ended)
}

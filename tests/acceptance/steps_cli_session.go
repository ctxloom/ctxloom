//go:build acceptance

package acceptance

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/cucumber/godog"
)

// Fixtures for the per-noun `session` CLI spec (features/cli/session.feature).
//
// The shared "a recorded session" step seeds an index entry and an essence,
// which is all a READING scenario needs. Every destroyer needs two things it
// does not provide: an ended_at (each one refuses a session that may still be
// writing) and a real transcript on disk (a destroyer with nothing to destroy
// proves nothing about destroying).
func registerCLISessionSteps(ctx *godog.ScenarioContext) {
	ctx.Step(`^a finished session "([^"]*)" with a transcript and an essence$`, func(c context.Context, harp string) error {
		return seedFinishedSession(c, harp, true)
	})

	ctx.Step(`^a finished session "([^"]*)" with a transcript and no essence$`, func(c context.Context, harp string) error {
		return seedFinishedSession(c, harp, false)
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

	// Fixtures for `session adopt` (features/cli/session.feature's "Adopt"
	// rule): a harp bound to a real claude-shaped vendor transcript, an extra
	// vendor file sitting in that SAME directory the index never recorded
	// (the orphan `session adopt` exists to find), and a bare backend-only
	// seed for the unsupported-backend refusal — none of which need an
	// ended_at or an essence, since adopt reads the vendor store directly
	// rather than any destroyer's on-disk artifacts.
	ctx.Step(`^a recorded session "([^"]*)" with a claude vendor transcript spanning "([^"]*)" to "([^"]*)"$`, func(c context.Context, harp, start, end string) error {
		return seedAdoptHarp(c, harp, "claude-code", harp+"-live", start, end)
	})

	ctx.Step(`^an orphaned claude vendor transcript "([^"]*)" for "([^"]*)" spanning "([^"]*)" to "([^"]*)"$`, func(c context.Context, candidateID, _, start, end string) error {
		return writeAdoptVendorHomeFile(c, candidateID, start, end)
	})

	ctx.Step(`^a recorded session "([^"]*)" for backend "([^"]*)"$`, func(c context.Context, harp, backend string) error {
		w := worldFrom(c)
		index := fmt.Sprintf("sessions:\n"+
			"  - harp_name: %s\n"+
			"    session_id: %s-live\n"+
			"    backend: %s\n"+
			"    project_dir: %s\n"+
			"    started_at: 2026-01-01T00:00:00Z\n"+
			"    transcript_path: %s\n",
			harp, harp, backend, w.env.ProjectDir, filepath.Join(w.env.HomeDir, adoptVendorDirRel, harp+"-live.jsonl"))
		return w.env.WriteHomeFile(filepath.Join(".ctxloom", "sessions", "index.yaml"), index)
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

// seedAdoptHarp writes an index entry bound to a real vendor transcript
// (sessionID.jsonl under adoptVendorDirRel) spanning start to end, so
// `session adopt` has a live binding to scan alongside and a real directory
// to scan for orphans.
func seedAdoptHarp(c context.Context, harp, backend, sessionID, start, end string) error {
	w := worldFrom(c)
	transcriptPath := filepath.Join(w.env.HomeDir, adoptVendorDirRel, sessionID+".jsonl")
	index := fmt.Sprintf("sessions:\n"+
		"  - harp_name: %s\n"+
		"    session_id: %s\n"+
		"    backend: %s\n"+
		"    project_dir: %s\n"+
		"    started_at: 2026-01-01T00:00:00Z\n"+
		"    transcript_path: %s\n",
		harp, sessionID, backend, w.env.ProjectDir, transcriptPath)
	if err := w.env.WriteHomeFile(filepath.Join(".ctxloom", "sessions", "index.yaml"), index); err != nil {
		return err
	}
	return writeAdoptVendorHomeFile(c, sessionID, start, end)
}

// seedFinishedSession writes an ENDED index entry for harp plus a real
// canonical transcript, and (when distilled) an essence. The transcript
// carries real bytes rather than an empty file: a purge reports the bytes it
// freed, and a zero-byte fixture cannot tell "freed the file" from "freed
// nothing".
func seedFinishedSession(c context.Context, harp string, distilled bool) error {
	w := worldFrom(c)
	sessionsRel := filepath.Join(".ctxloom", "sessions")
	harpRel := filepath.Join(sessionsRel, harp)
	transcriptRel := filepath.Join(harpRel, "persist", "transcript.jsonl")

	index := fmt.Sprintf("sessions:\n"+
		"  - harp_name: %s\n"+
		"    session_id: seeded-%s\n"+
		"    backend: claude-code\n"+
		"    project_dir: %s\n"+
		"    started_at: 2026-01-01T00:00:00Z\n"+
		"    ended_at: 2026-01-02T00:00:00Z\n"+
		"    transcript_path: %s\n"+
		"    summary: seeded acceptance session\n",
		harp, harp, w.env.ProjectDir, filepath.Join(w.env.HomeDir, transcriptRel))
	if err := w.env.WriteHomeFile(filepath.Join(sessionsRel, "index.yaml"), index); err != nil {
		return err
	}
	if err := w.env.WriteHomeFile(transcriptRel, `{"type":"user","content":"hello"}`+"\n"); err != nil {
		return err
	}
	if !distilled {
		return nil
	}
	essence := fmt.Sprintf("---\nharp_name: %s\ndistilled_at: 2026-01-02T00:00:00Z\n---\n\nSeeded essence for %s.\n", harp, harp)
	return w.env.WriteHomeFile(filepath.Join(harpRel, "essence.md"), essence)
}

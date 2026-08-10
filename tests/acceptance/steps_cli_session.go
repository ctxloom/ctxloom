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

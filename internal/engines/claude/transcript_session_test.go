package claude

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// TestTranscriptSession_IsTheFileStem: claude names each transcript
// <encoded-project>/<session-id>.jsonl, so a transcript found by location
// names its session by its stem — the key Instance.Resume takes.
func TestTranscriptSession_IsTheFileStem(t *testing.T) {
	kind, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "native", "projects", "-work-proj", "0b6f7c1e-2d9a-4c55-9a51-6b1f0d7e2a10.jsonl")
	got, err := kind.TranscriptSession(p)
	if err != nil {
		t.Fatalf("TranscriptSession(%q): %v", p, err)
	}
	if want := "0b6f7c1e-2d9a-4c55-9a51-6b1f0d7e2a10"; got != want {
		t.Fatalf("TranscriptSession = %q, want %q", got, want)
	}
}

// TestTranscriptSession_RefusesAForeignFile: a file that is not a .jsonl
// transcript (sessions.LocateTranscript falls back to a .json) names no
// claude session.
func TestTranscriptSession_RefusesAForeignFile(t *testing.T) {
	kind, err := Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/n/projects/-p/meta.json", "/n/projects/-p/.jsonl", "/n/projects/-p/noext"} {
		if id, err := kind.TranscriptSession(p); !errors.Is(err, engine.ErrForeignTranscript) {
			t.Errorf("TranscriptSession(%q) = %q, %v; want ErrForeignTranscript", p, id, err)
		}
	}
}

package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestLinkTranscriptIntoHarpDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkTranscriptIntoHarpDir("swift-amber-falcon", target)

	link := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", "transcript.jsonl")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", link, err)
	}
	if got != target {
		t.Fatalf("symlink target = %q, want %q", got, target)
	}

	// Idempotent: a second bind replaces the link without error.
	target2 := filepath.Join(t.TempDir(), "def.jsonl")
	if err := os.WriteFile(target2, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkTranscriptIntoHarpDir("swift-amber-falcon", target2)
	got, err = os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink after replace: %v", err)
	}
	if got != target2 {
		t.Fatalf("after replace, target = %q, want %q", got, target2)
	}
}

// TestLinkTranscriptIntoHarpDir_SkipsInSessionTarget: a transcript that
// already lives INSIDE the session dir (the containerized case — the engine's
// store root is bind-mounted at persist/transcripts) gets no reference link;
// it is harp-addressable by location and a link would just be a second name
// inside the same dir.
func TestLinkTranscriptIntoHarpDir_SkipsInSessionTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	harpDir := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon")
	inside := filepath.Join(harpDir, "persist", "transcripts", "enc", "abc.jsonl")
	if err := os.MkdirAll(filepath.Dir(inside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkTranscriptIntoHarpDir("swift-amber-falcon", inside)

	if _, err := os.Lstat(filepath.Join(harpDir, "transcript.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no transcript.jsonl link for an in-session-dir transcript (err=%v)", err)
	}
}

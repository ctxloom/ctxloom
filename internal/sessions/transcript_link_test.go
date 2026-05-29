package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLinkTranscriptIntoHarpDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

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

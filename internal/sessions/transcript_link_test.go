package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLinkEngineTranscript creates the per-vendor-log symlink at the ruled
// name (paths.HarpEngineTranscriptLinkPath: engine-transcript-<engine>-
// <sessionID>.jsonl) pointing at the bound vendor transcript. This is the
// bind-creates-the-link mutation kill: drop the create and this goes red.
func TestLinkEngineTranscript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target)

	link := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", "engine-transcript-claude-code-sess-1.jsonl")
	got, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected symlink at %s: %v", link, err)
	}
	if got != target {
		t.Fatalf("symlink target = %q, want %q", got, target)
	}

	// Create-once: a repeat call for the SAME engine+sessionID+target is a
	// no-op, not an error and not a repoint (there is nothing to repoint to).
	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target)
	got, err = os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink after repeat call: %v", err)
	}
	if got != target {
		t.Fatalf("after repeat call, target = %q, want %q (unchanged)", got, target)
	}
}

// TestLinkEngineTranscript_RotationCreatesASeparateImmutableLink is the
// rotation/immutability pin: a second bind naming a DIFFERENT session id (the
// shape of a /clear rotation) creates its OWN link and leaves the first one
// completely untouched. Mutation: repoint or remove the first link on
// rotation -> this goes red.
func TestLinkEngineTranscript_RotationCreatesASeparateImmutableLink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target1 := filepath.Join(t.TempDir(), "before-clear.jsonl")
	require := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	require(os.WriteFile(target1, []byte("{}\n"), 0o644))
	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target1)

	harpDir := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon")
	link1 := filepath.Join(harpDir, "engine-transcript-claude-code-sess-1.jsonl")
	before, err := os.Readlink(link1)
	require(err)
	if before != target1 {
		t.Fatalf("first link target = %q, want %q", before, target1)
	}

	// The rotation: same harp, same engine, a NEW session id and transcript
	// (the shape /clear produces).
	target2 := filepath.Join(t.TempDir(), "after-clear.jsonl")
	require(os.WriteFile(target2, []byte("{}\n"), 0o644))
	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-2", target2)

	link2 := filepath.Join(harpDir, "engine-transcript-claude-code-sess-2.jsonl")
	after2, err := os.Readlink(link2)
	require(err)
	if after2 != target2 {
		t.Fatalf("second link target = %q, want %q", after2, target2)
	}

	// BOTH links exist afterwards, and the first is byte-for-byte unchanged —
	// the whole point of a per-vendor-log name.
	after1, err := os.Readlink(link1)
	require(err)
	if after1 != target1 {
		t.Fatalf("first link target after rotation = %q, want %q (must be untouched)", after1, target1)
	}
}

// TestLinkEngineTranscript_SkipsInSessionTarget: a transcript that already
// lives INSIDE the session dir (the containerized case — the engine's store
// root is bind-mounted at persist/transcripts) gets no reference link; it is
// harp-addressable by location and a link would just be a second name inside
// the same dir.
func TestLinkEngineTranscript_SkipsInSessionTarget(t *testing.T) {
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

	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", inside)

	if _, err := os.Lstat(filepath.Join(harpDir, "engine-transcript-claude-code-sess-1.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("expected no engine-transcript link for an in-session-dir transcript (err=%v)", err)
	}
}

// TestLinkEngineTranscript_OldRootSymlinkNeverCreated pins C12's retirement:
// after a fresh bind, the old bare <harp>/transcript.jsonl root symlink does
// NOT exist. Mutation: restore the old linkTranscriptIntoHarpDir call ->
// this goes red.
func TestLinkEngineTranscript_OldRootSymlinkNeverCreated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privilege on Windows; the feature is best-effort there")
	}
	home := testsupport.Isolate(t)

	target := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	linkEngineTranscript("swift-amber-falcon", "claude-code", "sess-1", target)

	oldRootLink := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon", paths.CanonicalTranscriptFileName)
	if _, err := os.Lstat(oldRootLink); !os.IsNotExist(err) {
		t.Fatalf("the retired root %s must not exist after a fresh bind (err=%v)", oldRootLink, err)
	}
}

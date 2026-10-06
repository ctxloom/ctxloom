package sessions

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// stemSessions is a TranscriptSessionFunc naming a transcript by its file
// stem, for the backend the tests mint under; any other backend is refused,
// so a test also proves which backend the derivation asked about.
func stemSessions(t *testing.T) {
	t.Helper()
	restore := UseTranscriptSessions(func(backend, transcript string) (string, error) {
		if backend != "claude-code" {
			return "", errors.New("unexpected backend " + backend)
		}
		return strings.TrimSuffix(filepath.Base(transcript), ".jsonl"), nil
	})
	t.Cleanup(restore)
}

// TestFind_LocatedTranscriptNamesTheSession: a containerized child's record
// carries at most the wire-learned native key (no transcript path — nothing
// in the child can write the controller's record). Its transcript is found BY
// LOCATION, and the session id is the one that transcript records, never a
// key the located file contradicts. A /clear rotation under the live process
// leaves a NEWER transcript, and the binding follows it on the next read.
func TestFind_LocatedTranscriptNamesTheSession(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	mgr, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	// The coordinator's wire bind: a key and no transcript path.
	if err := mgr.BindSession(entry.HarpName, "first", ""); err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	first := writeStoreFile(t, home, entry.HarpName, "-proj-enc/first.jsonl", base)

	got, err := mgr.Find(entry.HarpName)
	if err != nil || got == nil {
		t.Fatalf("find: %v (entry=%v)", err, got)
	}
	if got.SessionID != "first" || got.TranscriptPath != first {
		t.Fatalf("before rotation: session %q at %q, want %q at %q", got.SessionID, got.TranscriptPath, "first", first)
	}

	// /clear: the live process starts a new transcript.
	rotated := writeStoreFile(t, home, entry.HarpName, "-proj-enc/rotated.jsonl", base.Add(time.Minute))

	got, err = mgr.Find(entry.HarpName)
	if err != nil || got == nil {
		t.Fatalf("find after rotation: %v", err)
	}
	if got.SessionID != "rotated" || got.TranscriptPath != rotated {
		t.Fatalf("after rotation: session %q at %q, want %q at %q", got.SessionID, got.TranscriptPath, "rotated", rotated)
	}

	listed, err := mgr.ListForProject("/proj")
	if err != nil || len(listed) != 1 {
		t.Fatalf("list: %v (n=%d)", err, len(listed))
	}
	if listed[0].SessionID != "rotated" {
		t.Fatalf("ListForProject SessionID = %q, want the located %q", listed[0].SessionID, "rotated")
	}

	// Derived on read, never persisted: the sidecar keeps the wire key.
	sidecar, err := paths.HarpSidecarPath(entry.HarpName)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "rotated") {
		t.Fatalf("the located session id leaked into the persisted sidecar:\n%s", raw)
	}
}

// TestFindBySessionID_FindsALocatedSession: the session id a containerized
// child's transcript records resolves to its harp, though no sidecar holds it.
func TestFindBySessionID_FindsALocatedSession(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	mgr, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	writeStoreFile(t, home, entry.HarpName, "-proj-enc/located.jsonl", time.Now().Add(-time.Minute))

	got, err := mgr.FindBySessionID("located")
	if err != nil || got == nil {
		t.Fatalf("FindBySessionID(located) = %v, %v; want harp %s", got, err, entry.HarpName)
	}
	if got.HarpName != entry.HarpName {
		t.Fatalf("FindBySessionID resolved %q, want %q", got.HarpName, entry.HarpName)
	}
}

// TestFind_BoundTranscriptKeepsItsSession: a host session's SessionStart bind
// recorded both halves; a store file beside it never re-derives the id.
func TestFind_BoundTranscriptKeepsItsSession(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	mgr, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(t.TempDir(), "bound.jsonl")
	if err := os.WriteFile(bound, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := mgr.BindSession(entry.HarpName, "hook-bound", bound); err != nil {
		t.Fatal(err)
	}
	writeStoreFile(t, home, entry.HarpName, "-proj-enc/elsewhere.jsonl", time.Now())

	got, err := mgr.Find(entry.HarpName)
	if err != nil || got == nil {
		t.Fatalf("find: %v", err)
	}
	if got.SessionID != "hook-bound" {
		t.Fatalf("SessionID = %q, want the hook's binding %q", got.SessionID, "hook-bound")
	}
}

// TestFind_UnnamedLocatedTranscriptKeepsTheRecord: an engine that cannot name
// a transcript's session (ErrUnsupported, a foreign file) leaves whatever the
// record holds; derivation never blanks a key.
func TestFind_UnnamedLocatedTranscriptKeepsTheRecord(t *testing.T) {
	home := testsupport.Isolate(t)
	t.Cleanup(UseTranscriptSessions(func(string, string) (string, error) {
		return "", errors.New("cannot name it")
	}))

	mgr, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.BindSession(entry.HarpName, "wire-key", ""); err != nil {
		t.Fatal(err)
	}
	writeStoreFile(t, home, entry.HarpName, "-proj-enc/x.jsonl", time.Now())

	got, err := mgr.Find(entry.HarpName)
	if err != nil || got == nil {
		t.Fatalf("find: %v", err)
	}
	if got.SessionID != "wire-key" {
		t.Fatalf("SessionID = %q, want the recorded %q", got.SessionID, "wire-key")
	}
}

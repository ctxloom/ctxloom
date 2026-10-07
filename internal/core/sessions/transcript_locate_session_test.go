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

// mintHarp opens the isolated home's store and mints one claude-code harp.
func mintHarp(t *testing.T) (*Manager, string) {
	t.Helper()
	mgr, err := Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	return mgr, entry.HarpName
}

// mustBind binds harp, failing the test on error.
func mustBind(t *testing.T, mgr *Manager, harp, sessionID, transcript string) {
	t.Helper()
	if err := mgr.BindSession(harp, sessionID, transcript); err != nil {
		t.Fatal(err)
	}
}

// requireBinding asserts the session id (and, when non-empty, the transcript)
// that Find reports for harp.
func requireBinding(t *testing.T, mgr *Manager, harp, wantID, wantPath string) {
	t.Helper()
	got, err := mgr.Find(harp)
	if err != nil || got == nil {
		t.Fatalf("find %s: %v (entry=%v)", harp, err, got)
	}
	if got.SessionID != wantID || (wantPath != "" && got.TranscriptPath != wantPath) {
		t.Fatalf("Find: session %q at %q, want %q at %q", got.SessionID, got.TranscriptPath, wantID, wantPath)
	}
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
	mgr, harp := mintHarp(t)
	// The coordinator's wire bind: a key and no transcript path.
	mustBind(t, mgr, harp, "first", "")
	base := time.Now().Add(-time.Hour)
	first := writeStoreFile(t, home, harp, "-proj-enc/first.jsonl", base)
	requireBinding(t, mgr, harp, "first", first)

	// /clear: the live process starts a new transcript.
	rotated := writeStoreFile(t, home, harp, "-proj-enc/rotated.jsonl", base.Add(time.Minute))
	requireBinding(t, mgr, harp, "rotated", rotated)

	listed, err := mgr.ListForProject("/proj")
	if err != nil || len(listed) != 1 || listed[0].SessionID != "rotated" {
		t.Fatalf("ListForProject = %+v, %v; want one entry bound to %q", listed, err, "rotated")
	}

	// Derived on read, never persisted: the sidecar keeps the wire key.
	sidecar, err := paths.HarpSidecarPath(harp)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(sidecar); err != nil || strings.Contains(string(raw), "rotated") {
		t.Fatalf("sidecar must keep only the recorded key (err=%v):\n%s", err, raw)
	}
}

// TestFindBySessionID_FindsALocatedSession: the session id a containerized
// child's transcript records resolves to its harp, though no sidecar holds it.
func TestFindBySessionID_FindsALocatedSession(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	mgr, harp := mintHarp(t)
	writeStoreFile(t, home, harp, "-proj-enc/located.jsonl", time.Now().Add(-time.Minute))

	got, err := mgr.FindBySessionID("located")
	if err != nil || got == nil || got.HarpName != harp {
		t.Fatalf("FindBySessionID(located) = %v, %v; want harp %s", got, err, harp)
	}
}

// TestFindBySessionID_LocatedSessionAnswersOnlyForItsOwnID: locating a
// transcript is not a match; the id it records must be the one asked for.
func TestFindBySessionID_LocatedSessionAnswersOnlyForItsOwnID(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	_, harp := mintHarp(t)
	mgr, _ := mintHarp(t)
	writeStoreFile(t, home, harp, "-proj-enc/located.jsonl", time.Now().Add(-time.Minute))

	got, err := mgr.FindBySessionID("someone-else")
	if err != nil || got != nil {
		t.Fatalf("FindBySessionID(someone-else) = %v, %v; want no entry", got, err)
	}
}

// TestFind_BoundTranscriptKeepsItsSession: a host session's SessionStart bind
// recorded both halves; a store file beside it never re-derives the id.
func TestFind_BoundTranscriptKeepsItsSession(t *testing.T) {
	home := testsupport.Isolate(t)
	stemSessions(t)

	mgr, harp := mintHarp(t)
	bound := filepath.Join(t.TempDir(), "bound.jsonl")
	if err := os.WriteFile(bound, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustBind(t, mgr, harp, "hook-bound", bound)
	writeStoreFile(t, home, harp, "-proj-enc/elsewhere.jsonl", time.Now())
	requireBinding(t, mgr, harp, "hook-bound", bound)
}

// TestFind_UnnamedLocatedTranscriptKeepsTheRecord: an engine that cannot name
// a transcript's session (ErrUnsupported, a foreign file) leaves whatever the
// record holds; derivation never blanks a key.
func TestFind_UnnamedLocatedTranscriptKeepsTheRecord(t *testing.T) {
	home := testsupport.Isolate(t)
	t.Cleanup(UseTranscriptSessions(func(string, string) (string, error) {
		return "", errors.New("cannot name it")
	}))

	mgr, harp := mintHarp(t)
	mustBind(t, mgr, harp, "wire-key", "")
	writeStoreFile(t, home, harp, "-proj-enc/x.jsonl", time.Now())
	requireBinding(t, mgr, harp, "wire-key", "")
}

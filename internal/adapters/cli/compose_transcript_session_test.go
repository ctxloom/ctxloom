package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestComposeEngines_LocatedTranscriptNamesTheSession: the composition root
// hands the sessions store the engines' own TranscriptSession, so a harp
// bound only by location (a containerized child's) reads back with the
// session its newest transcript records — on the reader resume uses.
func TestComposeEngines_LocatedTranscriptNamesTheSession(t *testing.T) {
	testsupport.Isolate(t)
	if err := composeEngines(); err != nil {
		t.Fatal(err)
	}
	mgr, err := sessions.Open(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := mgr.AssignHarp("/proj", "claude-code")
	if err != nil {
		t.Fatal(err)
	}
	native, err := paths.HarpNativeDir(entry.HarpName)
	if err != nil {
		t.Fatal(err)
	}
	const id = "6c3e1f0a-9b2d-4e7f-8a15-2d4c6b8e0f13"
	p := filepath.Join(native, "projects", "-proj", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := operations.GetSession(entry.HarpName)
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v (entry=%v)", err, got)
	}
	if got.SessionID != id {
		t.Fatalf("SessionID = %q, want %q, the located transcript's", got.SessionID, id)
	}
}

// TestBindSessionFromPayload_ContainerChildViewWritesNothing: a containerized
// child sees its harp's native dir (mounted, so its engine's history lands on
// the host) and never the controller's record. Its SessionStart hook must
// write nothing there — not a record, not a lock — because the controller
// binds that child by location (sessions.LocateTranscript and
// engine.Engine.TranscriptSession), never from a child-written record.
func TestBindSessionFromPayload_ContainerChildViewWritesNothing(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "brisk-teal-otter"
	native, err := paths.HarpNativeDir(harp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(native, "projects", "-proj"), 0o755); err != nil {
		t.Fatal(err)
	}

	payload := `{"session_id":"child-id","transcript_path":"` + filepath.Join(native, "projects", "-proj", "child-id.jsonl") + `"}`
	if err := bindSessionFromPayload(strings.NewReader(payload), claudeCodec(t), harp); err != nil {
		t.Fatalf("bind: %v", err)
	}

	dirents, err := os.ReadDir(filepath.Dir(native))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirents {
		if d.Name() != filepath.Base(native) {
			t.Errorf("the child's hook wrote %q beside its native dir; it must write nothing there", d.Name())
		}
	}
}

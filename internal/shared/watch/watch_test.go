package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recv waits for one event, failing on a watch error or timeout.
func recv(t *testing.T, w *Watcher) Event {
	t.Helper()
	select {
	case ev := <-w.Events():
		return ev
	case err := <-w.Errors():
		t.Fatalf("watch error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

// A write to the single watched file in a directory is reported, and unrelated
// siblings (e.g. the .lock) are filtered out — the taskloom task-log case.
func TestWatch_FileInDir_Filtered(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "proj.jsonl")
	w, err := New(dir, false, func(p string) bool { return p == target })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	// A filtered-out sibling must not produce an event before the real one.
	if err := os.WriteFile(filepath.Join(dir, "proj.jsonl.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, w); ev.Path != target {
		t.Fatalf("event path = %q, want %q", ev.Path, target)
	}
}

// A *.plan.md created in a pre-existing subdirectory is reported under a
// recursive watch — the ctxloom session-plans case.
func TestWatch_Recursive_Subdir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "swift-amber-falcon")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := New(root, true, func(p string) bool { return strings.HasSuffix(p, ".plan.md") })
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	plan := filepath.Join(sub, "v1.plan.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ev := recv(t, w); ev.Path != plan {
		t.Fatalf("event path = %q, want %q", ev.Path, plan)
	}
}

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

// TestNew_FailsOnMissingRoot pins U132-F03: New used to silently MkdirAll the
// root it was asked to watch, so a nonexistent, typo'd, or wrongly-resolved
// root produced a healthy-looking watcher on an empty directory that streams
// zero events forever, at exit 0 — indistinguishable from a correct-but-quiet
// watch. New must fail instead; a caller that genuinely needs the directory
// to exist creates it explicitly, locally, where that intent is reviewable.
func TestNew_FailsOnMissingRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := New(root, false, func(p string) bool { return true })
	if err == nil {
		t.Fatal("New succeeded watching a directory that does not exist")
	}
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

// TestClose_IsIdempotent pins U132-F05: Close closed w.done unconditionally, so
// a SECOND call panicked with "close of closed channel". A watcher is handed to
// a caller that defers Close and to a stream that may also want to stop it, and
// neither can ask whether the other already did — an idempotent Close is the
// only shape under which "stop this watcher" is a safe thing for two owners to
// say. Close must also mean the pump has actually stopped: returning while the
// goroutine still forwards events reports a shutdown that has not happened.
func TestClose_IsIdempotent(t *testing.T) {
	w, err := New(t.TempDir(), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Close returned, so the pump is stopped and the event channel is closed —
	// a receive must not block.
	select {
	case _, ok := <-w.Events():
		if ok {
			t.Fatal("Events() delivered an event after Close returned")
		}
	default:
		t.Fatal("Events() still open after Close returned: the pump had not stopped")
	}
}

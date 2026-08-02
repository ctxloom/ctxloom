//go:build !windows

package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSyncDir pins the durability seam append() uses when it creates a log.
// fsync on the file makes its contents durable and says nothing
// about the directory entry naming it, so the entry needs its own flush;
// there is no way to observe an fsync from a test, so what is pinned here is
// that the seam EXISTS, that it reports success on a real directory, and that
// it reports failure rather than silently doing nothing when there is no
// directory to sync — the last being the property that keeps append()'s error
// meaningful.
func TestSyncDir(t *testing.T) {
	dir := t.TempDir()
	if err := syncDir(dir); err != nil {
		t.Fatalf("syncDir(%s) = %v, want nil", dir, err)
	}
	missing := filepath.Join(dir, "does-not-exist")
	if err := syncDir(missing); err == nil {
		t.Fatalf("syncDir(%s) = nil; a missing directory must not report success", missing)
	}
}

// TestAppend_CreatesLogInAFreshDirectory is the characterization half: the
// added directory flush must not change what append() does on the happy path
// — a first event into a not-yet-existing nested directory still lands and
// still folds back.
func TestAppend_CreatesLogInAFreshDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "taskloom.jsonl")
	s, err := OpenLog(path, "sess")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := s.AddWithTrigger("first event", "", ""); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log not written at %s: %v", path, err)
	}
	if !strings.Contains(string(raw), "first event") {
		t.Fatalf("log does not carry the event: %q", string(raw))
	}
	got, err := s.List(nil, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("list = %+v, err = %v", got, err)
	}
}

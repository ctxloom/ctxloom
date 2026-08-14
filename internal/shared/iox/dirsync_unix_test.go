//go:build !windows

package iox

import (
	"path/filepath"
	"testing"
)

// TestSyncDir pins the real platform seam behind syncDirFn: that it exists,
// succeeds on a real directory, and fails rather than silently doing
// nothing when there is no directory to sync — the property that keeps a
// Durable() failure meaningful. There is no way to observe an fsync from a
// test; what is pinned is that the seam reports success/failure honestly.
// Mirrors tasks/syncdir_unix_test.go's TestSyncDir, one of the two shims
// this generalizes.
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

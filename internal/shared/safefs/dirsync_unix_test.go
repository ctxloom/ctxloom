//go:build !windows

package safefs

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
)

// TestSyncDir pins the real platform seam behind syncDirFn: that it exists,
// succeeds on a real directory, and fails rather than silently doing
// nothing when there is no directory to sync — the property that keeps a
// Durable() failure meaningful. There is no way to observe an fsync from a
// test; what is pinned is that the seam reports success/failure honestly.
func TestSyncDir(t *testing.T) {
	dir := t.TempDir()
	if err := syncDir(afero.NewOsFs(), dir); err != nil {
		t.Fatalf("syncDir(%s) = %v, want nil", dir, err)
	}
	missing := filepath.Join(dir, "does-not-exist")
	if err := syncDir(afero.NewOsFs(), missing); err == nil {
		t.Fatalf("syncDir(%s) = nil; a missing directory must not report success", missing)
	}
}

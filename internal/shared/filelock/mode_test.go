//go:build !windows

package filelock_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/filelock"
)

// A lock file must never be group- or world-WRITABLE. Acquiring a lock opens
// the file O_RDWR, so its write bits are exactly the list of accounts that can
// take the lock — and, through that, block every other account's writes to the
// resource it protects. Widening this is a decision about who may block whom.
//
// The read bits are not asserted: umask only ever CLEARS bits, so a stricter
// host is free to drop them and this pin must not fight that.
func TestLockFile_IsNotGroupOrWorldWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "guard.lock")
	unlock, err := filelock.Lock(path)
	require.NoError(t, err)
	defer unlock()

	fi, err := os.Stat(path)
	require.NoError(t, err)
	if mode := fi.Mode().Perm(); mode&0o022 != 0 {
		t.Errorf("lock file mode = %#o, want no group/other write bits", mode)
	}

	di, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	if mode := di.Mode().Perm(); mode&0o022 != 0 {
		t.Errorf("lock directory mode = %#o, want no group/other write bits", mode)
	}
	// The directory must still be traversable by its owner, or the lock file
	// inside it is unreachable — this is the whole of the 0o755/0o644
	// difference, and it is not an asymmetry to be normalized away.
	if mode := di.Mode().Perm(); mode&0o100 == 0 {
		t.Errorf("lock directory mode = %#o, want the owner execute bit", mode)
	}
}

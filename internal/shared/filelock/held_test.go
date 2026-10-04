package filelock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Held is the kernel's answer: held while an open handle holds the lock,
// released the moment that handle closes (as when its process dies), and a
// lock file that does not exist is not held. The probe never creates one.
func TestHeld_IsTheKernelsAnswer(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "owner.lock")

	held, err := Held(lockPath)
	require.NoError(t, err)
	assert.False(t, held, "no lock file is no holder")
	assert.NoFileExists(t, lockPath, "a probe must not mint the lock file")

	holder := flock.New(lockPath)
	got, err := holder.TryLock()
	require.NoError(t, err)
	require.True(t, got)

	held, err = Held(lockPath)
	require.NoError(t, err)
	assert.True(t, held)

	require.NoError(t, holder.Close())
	held, err = Held(lockPath)
	require.NoError(t, err)
	assert.False(t, held, "a released lock is no holder, though its file remains")
	_, statErr := os.Stat(lockPath)
	assert.NoError(t, statErr)
}

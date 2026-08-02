package remote

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Save(nil) panicked. It reached guardDestructiveWrite, which asks
// the incoming lockfile whether it IsEmpty, and dereferenced a nil receiver on
// the way to the answer; had the guard let it past, the LockedAt stamp would
// have panicked instead.
//
// No production caller passes nil today -- every one of them hands over the
// result of Load (which never returns a nil lockfile with a nil error) or a
// struct literal -- so this is a latent fault, not a live one. It is still
// worth closing on this type: the lockfile is the sole on-disk record of every
// dependency pin, every user hold and every publisher retraction, and a panic
// mid-write is the one failure mode that leaves a caller no chance to report
// what it was doing. An error naming the nil is answerable; a stack trace from
// inside a write is not.
func TestLockfileManager_SaveNilIsAnErrorNotAPanic(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	var err error
	require.NotPanics(t, func() { err = manager.Save(nil) })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil lockfile")

	// And nothing was written on the way to failing.
	exists, statErr := afero.Exists(fs, manager.Path())
	require.NoError(t, statErr)
	assert.False(t, exists, "a refused write must not leave a file behind")
}

// The refusal must not fire on a legitimately empty (but non-nil) lockfile:
// a genuinely empty project is a success, and AllowEmpty exists for the
// caller that means to empty a populated one.
func TestLockfileManager_SaveEmptyNonNilStillSucceeds(t *testing.T) {
	fs := afero.NewMemMapFs()
	manager := NewLockfileManager("/test", WithLockfileFS(fs))

	require.NoError(t, manager.Save(&Lockfile{Version: 1, Bundles: map[string]LockEntry{}}))

	exists, err := afero.Exists(fs, manager.Path())
	require.NoError(t, err)
	assert.True(t, exists)
}

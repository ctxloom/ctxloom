package safefs

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expired is a context already done: TryLock with it makes exactly one
// attempt.
func expired() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// The in-memory locks really serialize: N writers each doing an unlocked
// read-modify-write of one counter file would lose updates; under WithLock
// none is lost. This is what a test double that skipped locking could never
// show.
func TestNewMem_WithLockSerializesReadModifyWrite(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	const counter, lockPath, writers = "/state/counter", "/state/counter.lock", 32
	require.NoError(t, root.Fs.MkdirAll("/state", PrivateDirMode))
	require.NoError(t, afero.WriteFile(root.Fs, counter, []byte("0"), PrivateFileMode))

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			assert.NoError(t, WithLock(root.Locks, lockPath, func() error {
				raw, err := afero.ReadFile(root.Fs, counter)
				if err != nil {
					return err
				}
				n, err := strconv.Atoi(string(raw))
				if err != nil {
					return err
				}
				return afero.WriteFile(root.Fs, counter, []byte(strconv.Itoa(n+1)), PrivateFileMode)
			}))
		}()
	}
	close(start)
	wg.Wait()
	raw, err := afero.ReadFile(root.Fs, counter)
	require.NoError(t, err)
	assert.Equal(t, strconv.Itoa(writers), string(raw))
}

// WithLock never skips: inside fn the lock is held, so a second taker on the
// same path is refused — on an in-memory fs, where the old seam ran fn
// unlocked.
func TestWithLock_HoldsTheLockForFnOnMem(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	ran := false
	require.NoError(t, WithLock(root.Locks, "/l/x.lock", func() error {
		ran = true
		_, err := root.Locks.TryLock(expired(), "/l/x.lock")
		assert.ErrorIs(t, err, ErrLockHeld)
		held, err := root.Locks.Held("/l/x.lock")
		require.NoError(t, err)
		assert.True(t, held)
		return nil
	}))
	assert.True(t, ran)
	held, err := root.Locks.Held("/l/x.lock")
	require.NoError(t, err)
	assert.False(t, held, "WithLock releases when fn returns")
}

// fn's error is WithLock's, and the lock is still released.
func TestWithLock_ReturnsFnErrorAndReleases(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	boom := errors.New("boom")
	require.ErrorIs(t, WithLock(root.Locks, "/l/x.lock", func() error { return boom }), boom)
	l, err := root.Locks.TryLock(expired(), "/l/x.lock")
	require.NoError(t, err)
	require.NoError(t, l.Unlock())
}

// A blocked Lock is granted the moment the holder releases it.
func TestNewMem_LockWaitsForTheHolder(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	first, err := root.Locks.Lock("/l/x.lock")
	require.NoError(t, err)

	got := make(chan Lock)
	go func() {
		l, err := root.Locks.Lock("/l/x.lock")
		assert.NoError(t, err)
		got <- l
	}()
	select {
	case <-got:
		t.Fatal("a second Lock was granted while the first was held")
	default:
	}
	require.NoError(t, first.Unlock())
	second := <-got
	require.NotNil(t, second)
	require.NoError(t, second.Unlock())
}

// Two Roots NewMem builds over ONE fs see each other's locks, as two
// processes on one disk do.
func TestNewMem_RootsOverOneFsShareLocks(t *testing.T) {
	mfs := afero.NewMemMapFs()
	a, b := NewMem(mfs), NewMem(mfs)
	l, err := a.Locks.Lock("/l/x.lock")
	require.NoError(t, err)
	_, err = b.Locks.TryLock(expired(), "/l/x.lock")
	require.ErrorIs(t, err, ErrLockHeld)
	require.NoError(t, l.Unlock())
	_, err = NewMem(afero.NewMemMapFs()).Locks.TryLock(expired(), "/other/x.lock")
	require.ErrorIs(t, err, fs.ErrNotExist, "a different fs shares nothing, not even the directory")
}

// TryLock waits out a holder until its context ends, then reports
// ErrLockHeld; and it never creates a missing parent directory.
func TestNewMem_TryLock(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	_, err := root.Locks.TryLock(context.Background(), "/missing/x.lock")
	require.ErrorIs(t, err, fs.ErrNotExist)
	exists, _ := afero.DirExists(root.Fs, "/missing")
	assert.False(t, exists, "TryLock must not create the parent")

	require.NoError(t, root.Fs.MkdirAll("/d", PrivateDirMode))
	l, err := root.Locks.TryLock(expired(), "/d/x.lock")
	require.NoError(t, err, "one attempt is made even with a context already done")
	ctx, cancel := context.WithCancel(context.Background())
	released := make(chan error)
	go func() {
		w, err := root.Locks.TryLock(ctx, "/d/x.lock")
		if err == nil {
			err = w.Unlock()
		}
		released <- err
	}()
	cancel()
	require.ErrorIs(t, <-released, ErrLockHeld)
	require.NoError(t, l.Unlock())
}

// Current reports whether the locked file is still the one at its path: a
// removal, or a removal and re-creation, makes it false.
func TestNewMem_Current(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	l, err := root.Locks.Lock("/d/x.lock")
	require.NoError(t, err)
	assert.True(t, l.Current())
	require.NoError(t, root.Fs.RemoveAll("/d"))
	assert.False(t, l.Current(), "removed")
	require.NoError(t, root.Fs.MkdirAll("/d", PrivateDirMode))
	require.NoError(t, afero.WriteFile(root.Fs, "/d/x.lock", nil, PrivateFileMode))
	assert.False(t, l.Current(), "a new file at the path is not the one locked")
	require.NoError(t, l.Unlock())
}

// Held never creates the lock file, and a non-regular file there is refused.
func TestNewMem_Held(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	held, err := root.Locks.Held("/d/x.lock")
	require.NoError(t, err)
	assert.False(t, held)
	exists, _ := afero.Exists(root.Fs, "/d/x.lock")
	assert.False(t, exists, "a probe must not mint the lock file")

	require.NoError(t, root.Fs.MkdirAll("/d/dir.lock", PrivateDirMode))
	_, err = root.Locks.Held("/d/dir.lock")
	require.ErrorIs(t, err, ErrNotRegularFile)
	_, err = root.Locks.Lock("/d/dir.lock")
	require.ErrorIs(t, err, ErrNotRegularFile)
}

// On NewMem, owner-only is the mode bits: Ensure creates PrivateDirMode and
// tightens a loose dir; Check refuses group or other bits.
func TestNewMem_Private(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	require.NoError(t, root.Private.Ensure("/a/b"))
	require.NoError(t, root.Private.Check("/a/b"))

	require.NoError(t, root.Fs.Chmod(filepath.FromSlash("/a/b"), 0o755))
	var exposed *ExposedError
	require.ErrorAs(t, root.Private.Check("/a/b"), &exposed)
	assert.Equal(t, filepath.FromSlash("/a/b"), filepath.FromSlash(exposed.Path))
	assert.Contains(t, exposed.Why, "mode")

	require.NoError(t, root.Private.Ensure("/a/b"))
	require.NoError(t, root.Private.Check("/a/b"))

	err := root.Private.Check("/absent")
	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.NotErrorAs(t, err, &exposed)
}

// Ensure restricts the entry the filesystem made, whatever spelling of the
// path it was handed: a MemMapFs files an entry under its cleaned path but
// looks Chmod's name up as given, so an uncleaned name (any "/a/b" on
// Windows, where Clean makes it `\a\b`) restricts nothing and fails.
func TestNewMem_PrivateRestrictsAnUncleanPath(t *testing.T) {
	root := NewMem(afero.NewMemMapFs())
	require.NoError(t, root.Private.Ensure("/a//b/"))
	require.NoError(t, root.Private.Check("/a/b"))
}

// Ensure restricts a dir it creates (empty, so nothing is walked), and an
// existing dir only when it is exposed: restricting propagates to every child
// on Windows, so doing it on every start would walk the whole tree each time.
func TestPrivate_EnsureRestrictsOnlyWhenExposed(t *testing.T) {
	mfs := afero.NewMemMapFs()
	restricts := 0
	p := privateOn{fs: mfs, violation: modeViolation, restrict: func(dir string) error {
		restricts++
		return mfs.Chmod(filepath.Clean(dir), PrivateDirMode)
	}}
	require.NoError(t, p.Ensure("/a"))
	assert.Equal(t, 1, restricts, "a dir Ensure creates is restricted as it is made")
	restricts = 0
	require.NoError(t, p.Ensure("/a"))
	assert.Zero(t, restricts, "an owner-only dir is left alone")

	require.NoError(t, mfs.Chmod(filepath.FromSlash("/a"), 0o750))
	require.NoError(t, p.Ensure("/a"))
	assert.Equal(t, 1, restricts, "an exposed dir is restricted")
	require.NoError(t, p.Check("/a"))
}

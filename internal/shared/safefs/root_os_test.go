package safefs

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/fileperm"
)

// lockEntryPoints is every way New's Locks opens a lock file for taking.
// Each must treat a lock path the same way: a refusal only one honours is a
// hole in the other.
var lockEntryPoints = map[string]func(lockPath string) error{
	"Lock": func(lockPath string) error {
		l, err := New().Locks.Lock(lockPath)
		if err != nil {
			return err
		}
		return l.Unlock()
	},
	"TryLock": func(lockPath string) error {
		_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
		l, err := New().Locks.TryLock(expired(), lockPath)
		if err != nil {
			return err
		}
		return l.Unlock()
	},
}

// A symlinked lock path is followed, not refused: a user or agent may link a
// lock file wherever they like, and the target's contents are untouched.
func TestNewLocks_FollowSymlinkedLockPath(t *testing.T) {
	for name, open := range lockEntryPoints {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			target := filepath.Join(dir, "precious")
			require.NoError(t, WriteFile(New().Fs, target, []byte("keep"), 0o600))
			lockPath := filepath.Join(dir, "x.lock")
			require.NoError(t, os.Symlink(target, lockPath))

			require.NoError(t, open(lockPath))
			got, err := os.ReadFile(target)
			require.NoError(t, err)
			assert.Equal(t, "keep", string(got))
		})
	}
}

func TestWithLock_ThroughASymlinkLocksItsTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.lock")
	lockPath := filepath.Join(dir, "x.lock")
	require.NoError(t, os.Symlink(target, lockPath))

	ran := false
	require.NoError(t, WithLock(New().Locks, lockPath, func() error {
		ran = true
		probe := newKernelLock(target, true)
		locked, err := probe.TryLock()
		_ = probe.Close()
		require.NoError(t, err)
		assert.False(t, locked, "the lock taken through the link must hold its target")
		return nil
	}))
	assert.True(t, ran)
	info, err := os.Lstat(target)
	require.NoError(t, err, "a dangling link's target is created")
	assert.True(t, info.Mode().IsRegular())
}

// Lock creates a missing lock file and its directory as a regular file.
func TestNewLocks_LockCreatesRegularLockFile(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "sub", "x.lock")
	l, err := New().Locks.Lock(lockPath)
	require.NoError(t, err)
	require.NoError(t, l.Unlock())
	info, err := os.Lstat(lockPath)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
}

// TryLock never creates the parent: a dir removed under a claimant is
// fs.ErrNotExist, which is how a claimant learns its dir was reaped.
func TestNewLocks_TryLockNeverCreatesTheParent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	_, err := New().Locks.TryLock(context.Background(), filepath.Join(dir, "x.lock"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	assert.NoDirExists(t, dir)
}

// TryLock contends with another handle's kernel lock on the same path — as
// another process's would — and is granted once that holder lets go.
func TestNewLocks_TryLockContendsWithAFlockHolder(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	holder := newKernelLock(lockPath, true)
	got, err := holder.TryLock()
	require.NoError(t, err)
	require.True(t, got)

	_, err = New().Locks.TryLock(expired(), lockPath)
	require.ErrorIs(t, err, ErrLockHeld)

	require.NoError(t, holder.Close())
	l, err := New().Locks.TryLock(expired(), lockPath)
	require.NoError(t, err)
	require.NoError(t, l.Unlock())
}

// Current is false once the locked file is no longer the one at its path —
// unlinked, as a removal under the lock leaves it.
func TestNewLocks_CurrentAfterUnlink(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "x.lock")
	l, err := New().Locks.Lock(lockPath)
	require.NoError(t, err)
	defer func() { _ = l.Unlock() }()
	assert.True(t, l.Current())
	if err := os.Remove(lockPath); err != nil {
		t.Skipf("this platform cannot unlink a locked file: %v", err)
	}
	assert.False(t, l.Current())
}

// Held is the kernel's answer: held while an open handle holds the lock,
// released the moment that handle closes, and a lock file that does not
// exist is not held. The probe never creates one.
func TestNewLocks_HeldIsTheKernelsAnswer(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "owner.lock")
	locks := New().Locks

	held, err := locks.Held(lockPath)
	require.NoError(t, err)
	assert.False(t, held, "no lock file is no holder")
	assert.NoFileExists(t, lockPath, "a probe must not mint the lock file")

	holder := newKernelLock(lockPath, true)
	got, err := holder.TryLock()
	require.NoError(t, err)
	require.True(t, got)

	held, err = locks.Held(lockPath)
	require.NoError(t, err)
	assert.True(t, held)

	require.NoError(t, holder.Close())
	held, err = locks.Held(lockPath)
	require.NoError(t, err)
	assert.False(t, held, "a released lock is no holder, though its file remains")
	assert.FileExists(t, lockPath)
}

// New's Private creates a missing directory owner-only, and a file then
// created inside it is owner-only too — on Windows by inheriting the
// directory's ACE.
func TestNewPrivate_EnsureCreatesAnOwnerOnlyDirWhoseFilesAreOwnerOnly(t *testing.T) {
	root := New()
	dir := filepath.Join(t.TempDir(), "a", "b")
	require.NoError(t, root.Private.Ensure(dir))
	f := filepath.Join(dir, "secret")
	require.NoError(t, WriteFile(root.Fs, f, []byte("x"), PrivateFileMode))

	fileperm.OwnerOnly(t, dir)
	fileperm.OwnerOnly(t, f)
	require.NoError(t, root.Private.Check(dir, f))
}

// A path that is not there is not "owner-only" and not "exposed": it is
// missing, and the caller is told so as fs.ErrNotExist.
func TestNewPrivate_CheckAMissingPathIsNotExist(t *testing.T) {
	err := New().Private.Check(filepath.Join(t.TempDir(), "absent"))
	require.ErrorIs(t, err, fs.ErrNotExist)
	var exposed *ExposedError
	assert.NotErrorAs(t, err, &exposed)
}

// New's Private restricts only an exposed dir, on the platform's own terms
// (loosen, below, is per platform).
func TestNewPrivate_EnsureRestrictsOnlyWhenExposed(t *testing.T) {
	restricts := 0
	p := osPrivate()
	inner := p.restrict
	p.restrict = func(dir string) error { restricts++; return inner(dir) }

	dir := filepath.Join(t.TempDir(), "private")
	require.NoError(t, p.Ensure(dir))
	restricts = 0
	require.NoError(t, p.Ensure(dir))
	assert.Zero(t, restricts, "an owner-only dir is left alone")

	loosen(t, dir)
	var exposed *ExposedError
	require.ErrorAs(t, p.Check(dir), &exposed)
	require.NoError(t, p.Ensure(dir))
	assert.Equal(t, 1, restricts, "an exposed dir is restricted")
	require.NoError(t, p.Check(dir))
}

// The Windows owner-only verdict, platform-neutrally: the owner and the
// tolerated machine principals (SYSTEM, Administrators) are not exposure;
// anyone else is, named once each.
func TestExposure(t *testing.T) {
	const owner, system, admins, everyone, users = "S-1-5-21-1", "S-1-5-18", "S-1-5-32-544", "S-1-1-0", "S-1-5-32-545"
	tolerated := []string{system, admins}
	for _, tc := range []struct {
		name     string
		grantees []string
		want     string
	}{
		{"owner only", []string{owner}, ""},
		{"owner with SYSTEM and Administrators is still owner-only", []string{owner, system, admins}, ""},
		{"no grantee at all", nil, ""},
		{"Everyone is exposure", []string{owner, everyone}, "grants access to " + everyone},
		{"each outsider named once, in ACL order", []string{users, owner, everyone, users}, "grants access to " + users + ", " + everyone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, exposure(owner, tolerated, tc.grantees))
		})
	}
}

// A lock's Unlock is safe to call twice, and the second call releases
// nothing: in particular not the lock the NEXT taker of the path now holds.
// A caller's deferred Unlock beside an explicit one must not hand the
// resource to a third taker while the second still works under it.
func TestLocks_UnlockTwiceReleasesOnlyItsOwnHold(t *testing.T) {
	roots := map[string]func(t *testing.T) (Root, string){
		"New": func(t *testing.T) (Root, string) { return New(), filepath.Join(t.TempDir(), "x.lock") },
		"NewMem": func(*testing.T) (Root, string) {
			return NewMem(afero.NewMemMapFs()), "/l/x.lock"
		},
	}
	for name, mk := range roots {
		t.Run(name, func(t *testing.T) {
			root, path := mk(t)
			first, err := root.Locks.Lock(path)
			require.NoError(t, err)
			require.NoError(t, first.Unlock())

			second, err := root.Locks.Lock(path)
			require.NoError(t, err)
			defer func() { _ = second.Unlock() }()

			require.NoError(t, first.Unlock(), "a second Unlock is not an error")
			_, err = root.Locks.TryLock(expired(), path)
			assert.ErrorIs(t, err, ErrLockHeld, "the second taker still holds the lock")
		})
	}
}

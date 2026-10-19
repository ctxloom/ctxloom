package safefs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardedRoots are both Locks implementations with a lock path whose
// directory does not exist yet, and a reader of that path's bytes.
func guardedRoots(t *testing.T) map[string]struct {
	locks Locks
	path  string
	read  func() ([]byte, error)
} {
	dir := t.TempDir()
	mem := afero.NewMemMapFs()
	osFs := afero.NewOsFs()
	osPath := filepath.Join(dir, "locks", "x.lock")
	return map[string]struct {
		locks Locks
		path  string
		read  func() ([]byte, error)
	}{
		"os":  {New().Locks, osPath, func() ([]byte, error) { return afero.ReadFile(osFs, osPath) }},
		"mem": {NewMem(mem).Locks, "/l/x.lock", func() ([]byte, error) { return afero.ReadFile(mem, "/l/x.lock") }},
	}
}

// A lock file LockGuarding creates records the path it guards, made
// absolute, and GuardedPath reads exactly that back.
func TestLockGuarding_ACreatedLockFileRecordsWhatItGuards(t *testing.T) {
	guarded := filepath.Join(t.TempDir(), "project", ".mcp.json")
	for name, r := range guardedRoots(t) {
		t.Run(name, func(t *testing.T) {
			lk, err := LockGuarding(r.locks, r.path, guarded)
			require.NoError(t, err)
			assert.True(t, lk.Current())
			require.NoError(t, lk.Unlock())

			content, err := r.read()
			require.NoError(t, err)
			got, ok := GuardedPath(content)
			require.True(t, ok, "no record in %q", content)
			assert.Equal(t, guarded, got)
		})
	}
}

// A lock file already there is taken as it is: an empty one — from a taker
// that records nothing, or an older build — stays empty, and LockGuarding
// still excludes every other taker.
func TestLockGuarding_LeavesAnExistingLockFileAsItIs(t *testing.T) {
	for name, r := range guardedRoots(t) {
		t.Run(name, func(t *testing.T) {
			first, err := r.locks.Lock(r.path)
			require.NoError(t, err)
			require.NoError(t, first.Unlock())

			lk, err := LockGuarding(r.locks, r.path, "/project/.mcp.json")
			require.NoError(t, err)
			_, err = r.locks.TryLock(expired(), r.path)
			require.ErrorIs(t, err, ErrLockHeld)
			require.NoError(t, lk.Unlock())

			content, err := r.read()
			require.NoError(t, err)
			assert.Empty(t, content, "an existing lock file was rewritten")
		})
	}
}

// A lock file reaped and taken again is created again, so it records what it
// guards again.
func TestLockGuarding_RecordsAgainAfterAReap(t *testing.T) {
	for name, r := range guardedRoots(t) {
		t.Run(name, func(t *testing.T) {
			lk, err := LockGuarding(r.locks, r.path, "/a/one")
			require.NoError(t, err)
			require.NoError(t, lk.Unlock())
			reaped, err := ReapLockFile(r.locks, r.path)
			require.NoError(t, err)
			require.True(t, reaped)

			lk, err = LockGuarding(r.locks, r.path, "/a/two")
			require.NoError(t, err)
			require.NoError(t, lk.Unlock())
			content, err := r.read()
			require.NoError(t, err)
			got, ok := GuardedPath(content)
			require.True(t, ok)
			want, err := filepath.Abs("/a/two")
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// plainLocks is a Locks that cannot record (it wraps another by embedding,
// as a test's tracker does) and counts the exclusive takes it sees.
type plainLocks struct {
	Locks
	takes int
}

func (p *plainLocks) Lock(path string) (Lock, error) {
	p.takes++
	return p.Locks.Lock(path)
}

// On a Locks that cannot record, LockGuarding is its own Lock: the take goes
// through it, and the lock file records nothing.
func TestLockGuarding_FallsBackToLockOnALocksThatCannotRecord(t *testing.T) {
	for name, r := range guardedRoots(t) {
		t.Run(name, func(t *testing.T) {
			p := &plainLocks{Locks: r.locks}
			lk, err := LockGuarding(p, r.path, "/project/.mcp.json")
			require.NoError(t, err)
			require.NoError(t, lk.Unlock())
			assert.Equal(t, 1, p.takes)
			content, err := r.read()
			require.NoError(t, err)
			assert.Empty(t, content)
		})
	}
}

// GuardedPath reads only a whole record: one absolute, clean path and its
// closing newline. Anything else — empty, caught half-written, relative,
// unclean, more than one line — states no path, and a reader must not guess
// one.
func TestGuardedPath_ReadsOnlyAWholeRecord(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "p", ".mcp.json")
	got, ok := GuardedPath(guardRecord(abs))
	require.True(t, ok)
	assert.Equal(t, abs, got)

	for name, content := range map[string]string{
		"empty":         "",
		"half-written":  abs[:len(abs)/2],
		"no newline":    abs,
		"relative":      "p/.mcp.json\n",
		"unclean":       abs + "/../x\n",
		"two lines":     abs + "\n" + abs + "\n",
		"embedded NUL":  abs + "\x00\n",
		"just newline":  "\n",
		"trailing CRLF": abs + "\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, ok := GuardedPath([]byte(content))
			assert.False(t, ok)
		})
	}
}

// TryRLock is RLock bounded by its context: granted beside other shared
// holders, refused (ErrLockHeld) while an exclusive holder outlasts the
// context, and — like RLock — it creates the lock file and its directory.
func TestLocks_TryRLockSharesAndIsBoundedByItsContext(t *testing.T) {
	for name, r := range guardedRoots(t) {
		t.Run(name, func(t *testing.T) {
			first, err := r.locks.TryRLock(expired(), r.path)
			require.NoError(t, err, "a missing lock file and directory were not created")
			second, err := r.locks.TryRLock(expired(), r.path)
			require.NoError(t, err, "a shared take was refused beside another")
			_, err = r.locks.TryLock(expired(), r.path)
			require.ErrorIs(t, err, ErrLockHeld, "an exclusive take was granted beside shared holders")
			require.NoError(t, first.Unlock())
			require.NoError(t, second.Unlock())

			writer, err := r.locks.Lock(r.path)
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err = r.locks.TryRLock(ctx, r.path)
			require.ErrorIs(t, err, ErrLockHeld, "a shared take was granted beside an exclusive holder")
			require.NoError(t, writer.Unlock())
		})
	}
}

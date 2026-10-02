package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingFs counts the operations that can change a path's content: an
// atomic write lands by Rename, a removal by Remove. Both are keyed by the
// DESTINATION path, so a test can say "this file was written exactly once".
type countingFs struct {
	afero.Fs
	mu      sync.Mutex
	renames map[string]int
	removes map[string]int
}

func newCountingFs() *countingFs {
	return &countingFs{Fs: afero.NewMemMapFs(), renames: map[string]int{}, removes: map[string]int{}}
}

func (c *countingFs) Rename(o, n string) error {
	c.mu.Lock()
	c.renames[n]++
	c.mu.Unlock()
	return c.Fs.Rename(o, n)
}

func (c *countingFs) Remove(n string) error {
	c.mu.Lock()
	c.removes[n]++
	c.mu.Unlock()
	return c.Fs.Remove(n)
}

func noLock(_ string, fn func() error) error { return fn() }

func appendText(s string) Edit {
	return func(cur []byte, _ bool) ([]byte, bool, error) {
		return append(append([]byte(nil), cur...), s...), true, nil
	}
}

func put(t *testing.T, fs afero.Fs, path, s string) {
	t.Helper()
	require.NoError(t, fs.MkdirAll("/p", 0o755))
	require.NoError(t, afero.WriteFile(fs, path, []byte(s), 0o644))
}

func get(t *testing.T, fs afero.Fs, path string) string {
	t.Helper()
	b, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	return string(b)
}

// The settling test for the accumulator: however many edits a path takes, a
// Commit writes it ONCE; a path whose edits change nothing is not written.
func TestBatchWritesEachChangedPathOncePerCommit(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/a", "a")
	put(t, fs, "/p/same", "same")

	b := NewBatch(fs, noLock)
	b.Edit("/p/a", appendText("1"))
	b.Edit("/p/a", appendText("2"))
	b.Edit("/p/a", appendText("3"))
	b.Edit("/p/b", appendText("new"))
	b.Edit("/p/same", func(cur []byte, _ bool) ([]byte, bool, error) { return cur, true, nil })
	got, err := b.Commit()
	require.NoError(t, err)

	assert.Equal(t, 1, fs.renames["/p/a"], "three edits, one write")
	assert.Equal(t, 1, fs.renames["/p/b"])
	assert.Zero(t, fs.renames["/p/same"], "an unchanged path is not written")
	assert.Equal(t, "a123", get(t, fs, "/p/a"), "edits fold in the order they were staged")
	assert.Equal(t, "new", get(t, fs, "/p/b"))
	assert.Equal(t, Committed{Written: []string{"/p/a", "/p/b"}, Unchanged: []string{"/p/same"}}, got)
}

// Each edit sees the previous edit's output and the existence it decided.
func TestBatchEditsSeeThePreviousEditsResult(t *testing.T) {
	fs := newCountingFs()
	var seen []bool
	b := NewBatch(fs, noLock)
	b.Edit("/p/x", func(cur []byte, exists bool) ([]byte, bool, error) {
		seen = append(seen, exists)
		return []byte("one"), true, nil
	})
	b.Edit("/p/x", func(cur []byte, exists bool) ([]byte, bool, error) {
		seen = append(seen, exists)
		assert.Equal(t, "one", string(cur))
		return []byte("ignored"), false, nil
	})
	b.Edit("/p/x", func(cur []byte, exists bool) ([]byte, bool, error) {
		seen = append(seen, exists)
		assert.Empty(t, cur)
		return cur, exists, nil
	})
	got, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, []bool{false, true, false}, seen)
	assert.Equal(t, Committed{Unchanged: []string{"/p/x"}}, got, "absent before, absent after: nothing to do")
	assert.Zero(t, fs.renames["/p/x"]+fs.removes["/p/x"])
}

func TestBatchRemovesAPathAnEditDropped(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/gone", "x")
	b := NewBatch(fs, noLock)
	b.Edit("/p/gone", func([]byte, bool) ([]byte, bool, error) { return nil, false, nil })
	got, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, Committed{Removed: []string{"/p/gone"}}, got)
	_, err = fs.Stat("/p/gone")
	assert.True(t, os.IsNotExist(err))
}

// A seal runs after every edit has folded and BEFORE the write, so a record
// written in a seal is durable before its target changes (record-first).
func TestBatchSealsRunBeforeTheWriteWithBothImages(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/t", "old")
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", appendText("+"))
	var calls int
	b.Seal("/p/t", func(before []byte, existed bool, after []byte, keep bool) error {
		calls++
		assert.Equal(t, "old", string(before))
		assert.True(t, existed)
		assert.Equal(t, "old+!", string(after), "a seal sees every edit, including one staged after it")
		assert.True(t, keep)
		assert.Equal(t, "old", get(t, fs, "/p/t"), "the target is not written until every seal returns")
		return nil
	})
	b.Edit("/p/t", appendText("!"))
	b.Seal("/p/t", func(_ []byte, _ bool, after []byte, _ bool) error {
		calls++
		assert.Equal(t, "old+!", string(after), "seals see the result of EVERY edit, wherever they were staged")
		return nil
	})
	_, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
}

// A path whose bytes do not change is still SEALED: a record can change
// while its target does not (a second writer joining an entry the file
// already holds), and the seal is where that record is written. The target
// itself is not written.
func TestBatchSealsAnUnchangedPathWithoutWritingIt(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/t", "same")
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", func(cur []byte, e bool) ([]byte, bool, error) { return cur, e, nil })
	var sealed int
	b.Seal("/p/t", func(before []byte, existed bool, after []byte, keep bool) error {
		sealed++
		assert.Equal(t, "same", string(before))
		assert.Equal(t, "same", string(after))
		assert.True(t, existed && keep)
		return nil
	})
	got, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, 1, sealed)
	assert.Equal(t, Committed{Unchanged: []string{"/p/t"}}, got)
	assert.Zero(t, fs.renames["/p/t"])
}

// A path with a seal and no edit is sealed with the file as it stands.
func TestBatchASealAloneSealsTheFileAsItStands(t *testing.T) {
	fs := newCountingFs()
	b := NewBatch(fs, noLock)
	var sealed int
	b.Seal("/p/t", func(before []byte, existed bool, after []byte, keep bool) error {
		sealed++
		assert.False(t, existed || keep)
		return nil
	})
	got, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, 1, sealed)
	assert.Equal(t, Committed{Unchanged: []string{"/p/t"}}, got)
}

// A failed edit on ANY path aborts before ANY path is written — including a
// path that sorts, and so commits, ahead of the failing one.
func TestBatchAFailedEditWritesNothing(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/a", "a")
	boom := errors.New("boom")
	b := NewBatch(fs, noLock)
	b.Edit("/p/a", appendText("1"))
	b.Edit("/p/z", func([]byte, bool) ([]byte, bool, error) { return nil, false, boom })
	_, err := b.Commit()
	require.ErrorIs(t, err, boom)
	assert.Equal(t, "a", get(t, fs, "/p/a"))
	assert.Zero(t, fs.renames["/p/a"])
}

func TestBatchAFailedSealLeavesItsTargetUnwritten(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/t", "old")
	boom := errors.New("record refused")
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", appendText("+"))
	b.Seal("/p/t", func([]byte, bool, []byte, bool) error { return boom })
	_, err := b.Commit()
	require.ErrorIs(t, err, boom)
	assert.Equal(t, "old", get(t, fs, "/p/t"))
}

// The empty-write guard holds: zero bytes over an existing file is refused
// unless the batch was built with AllowEmpty.
func TestBatchKeepsTheEmptyWriteGuard(t *testing.T) {
	empty := func([]byte, bool) ([]byte, bool, error) { return []byte{}, true, nil }

	fs := newCountingFs()
	put(t, fs, "/p/t", "x")
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", empty)
	_, err := b.Commit()
	require.ErrorIs(t, err, ErrEmptyOverwrite)
	assert.Equal(t, "x", get(t, fs, "/p/t"))

	b = NewBatch(fs, noLock, AllowEmpty())
	b.Edit("/p/t", empty)
	_, err = b.Commit()
	require.NoError(t, err)
	assert.Equal(t, "", get(t, fs, "/p/t"))
}

// Every path is locked, in sorted order, for the whole fold: the lock spans
// the read, every edit, the seals and the write.
func TestBatchHoldsEveryPathsLockInSortedOrder(t *testing.T) {
	fs := newCountingFs()
	var events []string
	held := map[string]bool{}
	lock := func(path string, fn func() error) error {
		events = append(events, "lock "+path)
		held[path] = true
		err := fn()
		held[path] = false
		return err
	}
	b := NewBatch(fs, lock)
	for _, p := range []string{"/p/c", "/p/a", "/p/b"} {
		p := p
		b.Edit(p, func(cur []byte, _ bool) ([]byte, bool, error) {
			assert.True(t, held["/p/a"] && held["/p/b"] && held["/p/c"], "every lock is held while %s folds", p)
			return []byte(p), true, nil
		})
		b.Seal(p, func([]byte, bool, []byte, bool) error {
			assert.True(t, held[p], "the seal for %s runs under its lock", p)
			return nil
		})
	}
	_, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, []string{"lock /p/a", "lock /p/b", "lock /p/c"}, events)
}

// On the OS fs, where a temp file cannot be created in a missing directory.
func TestBatchCreatesAMissingParentAndAPrivateFile(t *testing.T) {
	fs := afero.NewOsFs()
	path := filepath.Join(t.TempDir(), "deep", "er", "f")
	b := NewBatch(fs, noLock)
	b.Edit(path, appendText("x"))
	_, err := b.Commit()
	require.NoError(t, err)
	info, err := fs.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a file the batch creates is owner-only")
}

func TestBatchKeepsAnExistingFilesMode(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/t", "x")
	require.NoError(t, fs.Chmod("/p/t", 0o644))
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", appendText("y"))
	_, err := b.Commit()
	require.NoError(t, err)
	info, err := fs.Stat("/p/t")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

func TestBatchDurableSyncsTheDirectory(t *testing.T) {
	var synced []string
	restore := SetSyncDirForTesting(func(dir string) error { synced = append(synced, dir); return nil })
	defer restore()
	fs := newCountingFs()
	b := NewBatch(fs, noLock, Durable())
	b.Edit("/p/t", appendText("x"))
	_, err := b.Commit()
	require.NoError(t, err)
	assert.Contains(t, synced, "/p")

	synced = nil
	b = NewBatch(fs, noLock)
	b.Edit("/p/t", appendText("y"))
	_, err = b.Commit()
	require.NoError(t, err)
	assert.Empty(t, synced)
}

// A lock that fails is the commit's failure, and nothing under it is written.
func TestBatchALockFailureWritesNothing(t *testing.T) {
	fs := newCountingFs()
	boom := errors.New("lock busy")
	b := NewBatch(fs, func(path string, fn func() error) error {
		if path == "/p/b" {
			return boom
		}
		return fn()
	})
	b.Edit("/p/a", appendText("x"))
	b.Edit("/p/b", appendText("x"))
	_, err := b.Commit()
	require.ErrorIs(t, err, boom)
	assert.Zero(t, fs.renames["/p/a"])
}

// Committed's lists are sorted: callers report them, and a map-ordered list
// would make that report differ run to run.
func TestBatchCommittedListsAreSorted(t *testing.T) {
	fs := newCountingFs()
	b := NewBatch(fs, noLock)
	for _, p := range []string{"/p/z", "/p/m", "/p/a"} {
		b.Edit(p, appendText("x"))
	}
	got, err := b.Commit()
	require.NoError(t, err)
	assert.True(t, sort.StringsAreSorted(got.Written))
	assert.Len(t, got.Written, 3)
}

// A read that fails for any reason but absence is the commit's failure: it is
// not mistaken for "the file does not exist", which would rewrite it whole.
func TestBatchAReadFailureIsNotAbsence(t *testing.T) {
	fs := &unreadableFs{countingFs: newCountingFs()}
	put(t, fs, "/p/t", "x")
	b := NewBatch(fs, noLock)
	b.Edit("/p/t", appendText("y"))
	_, err := b.Commit()
	require.ErrorIs(t, err, os.ErrPermission)
	assert.Zero(t, fs.renames["/p/t"])
}

// unreadableFs refuses every open for reading, as an unreadable file would.
type unreadableFs struct{ *countingFs }

func (u *unreadableFs) Open(string) (afero.File, error) { return nil, os.ErrPermission }

func TestBatchDurableSyncsTheDirectoryOfARemoval(t *testing.T) {
	var synced []string
	restore := SetSyncDirForTesting(func(dir string) error { synced = append(synced, dir); return nil })
	defer restore()
	fs := newCountingFs()
	put(t, fs, "/p/t", "x")
	b := NewBatch(fs, noLock, Durable())
	b.Edit("/p/t", func([]byte, bool) ([]byte, bool, error) { return nil, false, nil })
	_, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, []string{"/p"}, synced)
}

// Existence is content: an EMPTY file removed, or created, is a change even
// though its bytes compare equal to no file at all.
func TestBatchExistenceAloneIsAChange(t *testing.T) {
	fs := newCountingFs()
	put(t, fs, "/p/empty", "")
	b := NewBatch(fs, noLock, AllowEmpty())
	b.Edit("/p/empty", func([]byte, bool) ([]byte, bool, error) { return nil, false, nil })
	b.Edit("/p/new", func([]byte, bool) ([]byte, bool, error) { return []byte{}, true, nil })
	got, err := b.Commit()
	require.NoError(t, err)
	assert.Equal(t, Committed{Written: []string{"/p/new"}, Removed: []string{"/p/empty"}}, got)
	_, err = fs.Stat("/p/new")
	assert.NoError(t, err)
}

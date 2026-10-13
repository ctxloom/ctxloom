package safefs

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fold is Commit with nothing landed: every path read and folded as Commit
// would, each reported as it stands and as Commit would leave it, sorted —
// and no write, removal, seal or confirm, on a filesystem that refuses every
// change.
func TestBatchFoldReportsEachPathAsCommitWouldLeaveItAndChangesNothing(t *testing.T) {
	base := afero.NewMemMapFs()
	put(t, base, "/p/same", "same")
	put(t, base, "/p/edit", "a")
	put(t, base, "/p/gone", "gone")
	fs := afero.NewReadOnlyFs(base)
	b := NewBatch(fs, noLock)
	b.Edit("/p/same", func(cur []byte, exists bool) ([]byte, bool, error) { return cur, exists, nil })
	b.Edit("/p/edit", appendText("b"))
	b.Edit("/p/gone", func([]byte, bool) ([]byte, bool, error) { return nil, false, nil })
	b.Edit("/p/new", appendText("new"))
	sealed, confirmed := 0, 0
	for _, p := range []string{"/p/same", "/p/edit", "/p/gone", "/p/new"} {
		b.Seal(p, func([]byte, bool, []byte, bool) error { sealed++; return nil })
		b.Confirm(p, func() error { confirmed++; return nil })
	}

	folded, err := b.Fold()
	require.NoError(t, err)

	require.Equal(t, []Folded{
		{Path: "/p/edit", Before: []byte("a"), Existed: true, After: []byte("ab"), Keep: true},
		{Path: "/p/gone", Before: []byte("gone"), Existed: true, After: nil, Keep: false},
		{Path: "/p/new", Before: nil, Existed: false, After: []byte("new"), Keep: true},
		{Path: "/p/same", Before: []byte("same"), Existed: true, After: []byte("same"), Keep: true},
	}, folded)
	changed := map[string]bool{}
	for _, f := range folded {
		changed[f.Path] = f.Changed()
	}
	assert.Equal(t, map[string]bool{"/p/edit": true, "/p/gone": true, "/p/new": true, "/p/same": false}, changed)
	assert.Zero(t, sealed, "a fold runs no seal")
	assert.Zero(t, confirmed, "a fold runs no confirm")
	assert.Equal(t, "a", get(t, base, "/p/edit"))
	assert.Equal(t, "gone", get(t, base, "/p/gone"))
	exists, err := afero.Exists(base, "/p/new")
	require.NoError(t, err)
	assert.False(t, exists, "a fold creates nothing")
}

// A fold is taken under every path's lock, in Commit's sorted order, so it
// reads each file as no concurrent writer of it has half-changed it.
func TestBatchFoldHoldsEveryPathsLockInSortedOrder(t *testing.T) {
	var order []string
	b := NewBatch(afero.NewMemMapFs(), func(p string, fn func() error) error { order = append(order, p); return fn() })
	b.Edit("/p/b", appendText("b"))
	b.Edit("/p/a", appendText("a"))
	_, err := b.Fold()
	require.NoError(t, err)
	assert.Equal(t, []string{"/p/a", "/p/b"}, order)
}

// A failed edit fails the fold, as it fails a commit.
func TestBatchFoldAFailedEditFailsTheFold(t *testing.T) {
	boom := errors.New("boom")
	b := NewBatch(afero.NewMemMapFs(), noLock)
	b.Edit("/p/a", func([]byte, bool) ([]byte, bool, error) { return nil, false, boom })
	_, err := b.Fold()
	require.ErrorIs(t, err, boom)
}

// Existence alone is a change: an empty file the fold would remove, or one
// it would create empty, changed though its bytes did not.
func TestBatchFoldExistenceAloneIsAChange(t *testing.T) {
	assert.True(t, Folded{Existed: true, Keep: false}.Changed())
	assert.True(t, Folded{Existed: false, Keep: true}.Changed())
	assert.False(t, Folded{Existed: false, Keep: false}.Changed())
}

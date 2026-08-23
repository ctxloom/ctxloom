package countersign

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStore_Resolve_DistinguishesTheFourStates is the whole point of
// Store.Resolve: an approvals store is in exactly ONE of four states, and
// three of them are faults that must never be mistaken for the fourth.
// Before Resolve existed the type could only answer the Readable() BOOLEAN,
// which folds ABSENT (the dir is set and is not there — an approvals volume
// that failed to mount looks exactly like this) into READABLE-AND-EMPTY,
// and a caller holding one answer could not tell "nothing was ever recorded"
// from "everything recorded is out of reach".
func TestStore_Resolve_DistinguishesTheFourStates(t *testing.T) {
	t.Run("unconfigured: nobody supplied a directory", func(t *testing.T) {
		state, err := NewStore("", afero.NewMemMapFs()).Resolve()
		assert.Equal(t, StateUnconfigured, state)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no directory configured")
	})

	t.Run("absent: the directory is set and does not exist", func(t *testing.T) {
		state, err := NewStore("/store", afero.NewMemMapFs()).Resolve()
		assert.Equal(t, StateAbsent, state)
		require.Error(t, err, "an absent store is a fault state, not a value")
		assert.Contains(t, err.Error(), "/store")
	})

	t.Run("unreadable: the directory exists and cannot be listed", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/store", 0o755))
		wrapped := denyFs{Fs: fs, deny: map[string]error{"/store": errors.New("permission denied")}}

		state, err := NewStore("/store", wrapped).Resolve()
		assert.Equal(t, StateUnreadable, state)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permission denied")
	})

	t.Run("unreadable: a record inside cannot be read", func(t *testing.T) {
		signer, _ := testSigner(t)
		fs := afero.NewMemMapFs()
		require.NoError(t, NewStore("/store", fs).WriteRefReject("acme/tooling#fragments/x", signer))
		matches, err := afero.Glob(fs, "/store/*.sig")
		require.NoError(t, err)
		require.Len(t, matches, 1)
		wrapped := denyFs{Fs: fs, deny: map[string]error{matches[0]: errors.New("permission denied")}}

		state, rerr := NewStore("/store", wrapped).Resolve()
		assert.Equal(t, StateUnreadable, state)
		require.Error(t, rerr)
	})

	t.Run("readable: the directory exists and lists", func(t *testing.T) {
		fs := afero.NewMemMapFs()
		require.NoError(t, fs.MkdirAll("/store", 0o755))

		state, err := NewStore("/store", fs).Resolve()
		assert.Equal(t, StateReadable, state)
		assert.NoError(t, err, "the only state that may yield a trust verdict")
	})
}

// TestStore_Resolve_AbsentIsNotReadableAndEmpty is the one comparison the
// whole four-state split exists for, asserted directly rather than by
// reading two subtests side by side: a store whose directory was REMOVED
// after a decision was recorded in it must not resolve identically to a
// store that exists and holds nothing.
func TestStore_Resolve_AbsentIsNotReadableAndEmpty(t *testing.T) {
	signer, _ := testSigner(t)
	fs := afero.NewMemMapFs()
	recorded := NewStore("/store", fs)
	require.NoError(t, recorded.WriteRefReject("acme/tooling#fragments/x", signer))

	before, err := recorded.Resolve()
	require.NoError(t, err)
	require.Equal(t, StateReadable, before)

	require.NoError(t, fs.RemoveAll("/store"))

	after, aerr := recorded.Resolve()
	assert.Equal(t, StateAbsent, after, "a store directory that was removed must not resolve as readable")
	assert.Error(t, aerr)
	assert.NotEqual(t, before, after)

	empty := NewStore("/empty", fs)
	require.NoError(t, fs.MkdirAll("/empty", 0o755))
	emptyState, eerr := empty.Resolve()
	require.NoError(t, eerr)
	assert.NotEqual(t, emptyState, after,
		"readable-and-empty and absent must be distinguishable — they were the same answer before Resolve existed")
}

// TestStore_Resolve_NilStore reports the honest answer for a *Store nobody
// built: there is no store here, which is the UNCONFIGURED fault, not a
// readable one. Store.Readable keeps its own nil tolerance (a nil project
// store is a supported shape for callers that never built one) — this pins
// that the two answers are deliberately different, so a future reader does
// not "simplify" Readable into a bare Resolve delegation and silently turn
// every nil store into a deny-all.
func TestStore_Resolve_NilStore(t *testing.T) {
	var s *Store
	state, err := s.Resolve()
	assert.Equal(t, StateUnconfigured, state)
	assert.Error(t, err)
	assert.NoError(t, s.Readable(), "Readable's nil tolerance is a separate, deliberate contract")
}

package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A purged session whose transcript is re-captured is an ordinary session
// again: ClearPurged removes the mark from the sidecar, and a Manager that
// shares no memory with the one that cleared it reads it unmarked.
func TestManager_ClearPurged_PersistsToTheSidecar(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.MarkPurged(e.HarpName, time.Now()))
	sidecar := filepath.Join(m.root, e.HarpName, "session.yaml")
	raw, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	require.Contains(t, string(raw), "purged_at:", "the fixture must start marked")

	require.NoError(t, m.ClearPurged(e.HarpName))

	fresh, err := Open(nil)
	require.NoError(t, err)
	found, err := fresh.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Nil(t, found.PurgedAt)
	raw, err = os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "purged_at")
}

// Clearing a session that carries no mark writes nothing.
func TestManager_ClearPurged_UnmarkedWritesNothing(t *testing.T) {
	m := newManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	sidecar := filepath.Join(m.root, e.HarpName, "session.yaml")
	before, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	beforeInfo, err := os.Stat(sidecar)
	require.NoError(t, err)

	require.NoError(t, m.ClearPurged(e.HarpName))

	after, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	afterInfo, err := os.Stat(sidecar)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.True(t, os.SameFile(beforeInfo, afterInfo), "an unchanged sidecar is not rewritten")
	assert.ErrorIs(t, m.ClearPurged("no-such-harp"), ErrNotFound)
}

func TestMemStore_ClearPurged_MatchesTheManager(t *testing.T) {
	s := NewMemStore()
	e, err := s.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, s.MarkPurged(e.HarpName, time.Now()))

	require.NoError(t, s.ClearPurged(e.HarpName))

	found, err := s.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Nil(t, found.PurgedAt)
	assert.ErrorIs(t, s.ClearPurged("no-such-harp"), ErrNotFound)
}

func TestMemStore_MarkPurged_RefusesAnUnknownHarpLikeTheManager(t *testing.T) {
	assert.ErrorIs(t, NewMemStore().MarkPurged("no-such-harp", time.Now()), ErrNotFound)
	assert.ErrorIs(t, newManager(t).MarkPurged("no-such-harp", time.Now()), ErrNotFound)
}

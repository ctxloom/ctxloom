package sessions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The origin is decided by the mint's seed and nothing else: a one-shot is
// internal whatever its depth, a delegated child is an agent, and the rest is
// a human's session.
func TestSeedOrigin(t *testing.T) {
	cases := []struct {
		name string
		seed Seed
		want Origin
	}{
		{"a human's run", Seed{}, OriginSession},
		{"a delegated child", Seed{Depth: 1}, OriginAgent},
		{"an internal one-shot", Seed{OneShot: true}, OriginOneShot},
		{"a one-shot below a coordinator is still a one-shot", Seed{OneShot: true, Depth: 2}, OriginOneShot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.seed.Origin())
		})
	}
}

// The origin has to survive the FILE: the sweep that reads it runs in a later
// process, and an in-memory stamp would leave every one-shot reading as a
// human's session — never purgeable without a distill.
func TestRecordOrigin_PersistsToTheSidecar(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	require.NoError(t, m.RecordOrigin(e.HarpName, OriginOneShot))

	reopened, err := Open(nil)
	require.NoError(t, err)
	got, err := reopened.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, OriginOneShot, got.Origin)
}

// An unrecorded origin reads as empty, which every consumer treats as a
// human's session: the reading that never purges an undistilled transcript.
func TestRecordOrigin_UnrecordedIsEmpty(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Empty(t, got.Origin)
}

func TestRecordOrigin_UnknownHarpErrors(t *testing.T) {
	m, _ := newIndexManager(t)
	assert.Error(t, m.RecordOrigin("no-such-harp", OriginSession))
	assert.Error(t, NewMemStore().RecordOrigin("no-such-harp", OriginSession))
}

func TestMemStore_RecordOrigin(t *testing.T) {
	s := NewMemStore()
	e, err := s.AssignHarp("/proj", "")
	require.NoError(t, err)
	require.NoError(t, s.RecordOrigin(e.HarpName, OriginAgent))
	got, err := s.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, OriginAgent, got.Origin)
}

package sessions

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
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

// The mint stamp has to survive the FILE: the sweep that reads the origin
// runs in a later process, and an in-memory stamp would leave every one-shot
// reading as a human's session — never purgeable without a distill. The
// signature-check posture is read later for the same reason: to tell a session
// that ran without signature verification apart from one that did not.
func TestStampMint_PersistsToTheSidecar(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	require.NoError(t, m.StampMint(e.HarpName, MintStamp{Origin: OriginOneShot, SigCheckDisabled: true}))

	reopened, err := Open(nil)
	require.NoError(t, err)
	got, err := reopened.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, OriginOneShot, got.Origin)
	assert.True(t, got.SigCheckDisabled)

	sidecar, err := paths.HarpSidecarPath(e.HarpName)
	require.NoError(t, err)
	raw, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "sig_check_disabled: true", "the on-disk key is the contract a later reader greps for")
}

// An unrecorded stamp reads as empty, which every consumer treats as a
// human's session that ran with signatures verified.
func TestStampMint_UnrecordedIsEmpty(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Empty(t, got.Origin)
	assert.False(t, got.SigCheckDisabled)
}

// An enforced session writes no key at all, so the key's presence is itself
// the signal.
func TestStampMint_AnEnforcedSessionWritesNoKey(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.StampMint(e.HarpName, MintStamp{Origin: OriginSession}))
	sidecar, err := paths.HarpSidecarPath(e.HarpName)
	require.NoError(t, err)
	raw, err := os.ReadFile(sidecar)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "sig_check_disabled")
}

func TestStampMint_UnknownHarpErrors(t *testing.T) {
	m, _ := newIndexManager(t)
	assert.Error(t, m.StampMint("no-such-harp", MintStamp{Origin: OriginSession}))
	assert.Error(t, NewMemStore().StampMint("no-such-harp", MintStamp{Origin: OriginSession}))
}

func TestMemStore_StampMint(t *testing.T) {
	s := NewMemStore()
	e, err := s.AssignHarp("/proj", "")
	require.NoError(t, err)
	require.NoError(t, s.StampMint(e.HarpName, MintStamp{Origin: OriginAgent, SigCheckDisabled: true}))
	got, err := s.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, OriginAgent, got.Origin)
	assert.True(t, got.SigCheckDisabled)
}

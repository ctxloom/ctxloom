package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

func TestDirtyTreeCommitAcknowledged_AbsentRecordDefaultsFalse(t *testing.T) {
	fs := afero.NewMemMapFs()
	assert.False(t, DirtyTreeCommitAcknowledged(fs, "/proj/.ctxloom"))
}

func TestSetDirtyTreeCommitAck_GrantThenRevoke(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"

	require.NoError(t, SetDirtyTreeCommitAck(fs, appDir, true))
	assert.True(t, DirtyTreeCommitAcknowledged(fs, appDir))

	// PAYLOAD assertion, not just "no error": the record must actually be on
	// disk, at the documented path, not merely reported as granted in memory.
	data, err := afero.ReadFile(fs, paths.DirtyTreeCommitAckPath(appDir))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "the ack store file must not be empty after granting")

	require.NoError(t, SetDirtyTreeCommitAck(fs, appDir, false))
	assert.False(t, DirtyTreeCommitAcknowledged(fs, appDir), "revoking must flip the acknowledgement back to false")
}

// TestSetDirtyTreeCommitAck_TwoProjectsAreIndependent proves the record is
// PROJECT-scoped (per-checkout), not a single global flag: granting it for
// one appPath must not acknowledge a different one.
func TestSetDirtyTreeCommitAck_TwoProjectsAreIndependent(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, SetDirtyTreeCommitAck(fs, "/proj-a/.ctxloom", true))

	assert.True(t, DirtyTreeCommitAcknowledged(fs, "/proj-a/.ctxloom"))
	assert.False(t, DirtyTreeCommitAcknowledged(fs, "/proj-b/.ctxloom"),
		"a different project's checkout must not inherit another's acknowledgement")
}

// TestDirtyTreeCommitAckPath_LivesUnderState proves the record lives at
// paths.StateDir (the gitignored, local-only third tier), not config.yaml —
// the whole point of moving it out of the layered config chain.
func TestDirtyTreeCommitAckPath_LivesUnderState(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, SetDirtyTreeCommitAck(fs, appDir, true))

	exists, err := afero.Exists(fs, paths.ConfigPath(appDir))
	require.NoError(t, err)
	assert.False(t, exists, "granting the ack must never create/touch config.yaml")

	exists, err = afero.Exists(fs, paths.DirtyTreeCommitAckPath(appDir))
	require.NoError(t, err)
	assert.True(t, exists)
}

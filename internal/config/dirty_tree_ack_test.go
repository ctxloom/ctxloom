package config

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// TestDirtyTreeCommitAcknowledged_AbsentRecordWarnsNothing proves the
// ordinary "nobody has decided yet" case — the overwhelmingly common one,
// since most checkouts never grant this — stays QUIET. Only an unreadable
// record (a genuine fault) should warn; an absent one is not a fault and
// must not train a user to ignore this warning by printing it on every
// spawn attempt of a project that simply never opted in.
func TestDirtyTreeCommitAcknowledged_AbsentRecordWarnsNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	assert.False(t, DirtyTreeCommitAcknowledged(fs, "/proj/.ctxloom"))
	assert.Empty(t, buf.String(), "an absent record is the ordinary unasked case, not a fault — it must not warn")
}

// TestDirtyTreeCommitAcknowledged_UnreadableStoreWarnsAndDenies is the
// defect this fix closes: DirtyTreeCommitAcknowledged used to swallow the
// store's read error with no diagnostic at all — the only one of the
// project's five admission kinds that neither warned nor escalated, in
// violation of the "a refusal is loud" rule. A store that EXISTS but cannot
// be parsed (corrupt YAML here; a permissions failure looks the same to this
// function) may hold a real decision, so this still DENIES (fail closed,
// unchanged), but now names the file, the underlying error, and the command
// that re-records the decision — `ctxloom manage commit trust`, the only
// other writer of this record (see dirty_tree_ack.go's doc and
// operations/delegate.go's commitDirtyTree refusal, which names the same
// command).
func TestDirtyTreeCommitAcknowledged_UnreadableStoreWarnsAndDenies(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	ackPath := paths.DirtyTreeCommitAckPath(appDir)
	require.NoError(t, fs.MkdirAll(ackPath[:strings.LastIndex(ackPath, "/")], 0o700))
	require.NoError(t, afero.WriteFile(fs, ackPath, []byte("not: [valid: yaml"), 0o600))

	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	assert.False(t, DirtyTreeCommitAcknowledged(fs, appDir), "an unreadable store must still deny — fail closed")

	warning := buf.String()
	assert.Contains(t, warning, ackPath, "the warning must name the file that could not be read")
	assert.Contains(t, warning, "manage commit trust", "the warning must name how to re-record the decision")
}

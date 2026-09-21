package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// --- DOCTOR-CHECK-LEGACY-INDEX-y5 --------------------------------------------

// wantLegacyIndexRemedy is the fix every wrong-state assertion below expects,
// as a literal rather than the production constant so the wording itself is
// what is pinned: the sidecar under each harp directory is the record, so the only
// correct action on a pre-rename index is to remove it.
const wantLegacyIndexRemedy = "delete it; the sidecars are the record"

func TestDoctorCheckLegacyIndex_RightState_NoSessionsDirYet(t *testing.T) {
	testsupport.Isolate(t)
	check := doctorCheckLegacyIndex()
	assert.Equal(t, doctorLegacyIndexMarker, check.Marker)
	assert.Equal(t, DoctorOK, check.Status)
}

func TestDoctorCheckLegacyIndex_RightState_HarpTreeWithoutIndex(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := paths.HarpDir("amber-quiet-heron")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(harpDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(harpDir, paths.SessionSidecarFileName), []byte("backend: claude-code\n"), 0o644))

	check := doctorCheckLegacyIndex()
	assert.Equal(t, DoctorOK, check.Status)
	assert.NotContains(t, check.Detail, paths.IndexFileName, "a clean tree names no index")
}

// TestDoctorCheckLegacyIndex_WrongState_NamesTheIndexWithRemedy is this
// slice's gate: a pre-rename index.yaml at the sessions root is IGNORED by
// the sessions store (internal/core/sessions pins that), and doctor is the
// surface that says so — by path, with the remedy.
func TestDoctorCheckLegacyIndex_WrongState_NamesTheIndexWithRemedy(t *testing.T) {
	testsupport.Isolate(t)
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionsRoot, 0o755))
	stale := filepath.Join(sessionsRoot, paths.IndexFileName)
	require.NoError(t, os.WriteFile(stale, []byte("sessions:\n  - harp_name: ghost-row\n"), 0o644))

	check := doctorCheckLegacyIndex()
	assert.Equal(t, doctorLegacyIndexMarker, check.Marker)
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, stale, "the finding names the file by its full path")
	assert.Contains(t, check.Detail, wantLegacyIndexRemedy)

	_, statErr := os.Stat(stale)
	assert.NoError(t, statErr, "doctor reports; it does not delete")
}

// TestDoctorCheckLegacyIndex_WrongState_NamesTheMigrationMarkerToo: the
// marker an older binary's migration left behind is the same kind of
// leftover, and the same remedy applies. Both names are reported when both
// are present.
func TestDoctorCheckLegacyIndex_WrongState_NamesTheMigrationMarkerToo(t *testing.T) {
	testsupport.Isolate(t)
	sessionsRoot, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(sessionsRoot, 0o755))
	migrated := filepath.Join(sessionsRoot, paths.MigratedIndexFileName)
	require.NoError(t, os.WriteFile(migrated, []byte("sessions: []\n"), 0o644))

	check := doctorCheckLegacyIndex()
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, migrated)
	assert.Contains(t, check.Detail, wantLegacyIndexRemedy)

	stale := filepath.Join(sessionsRoot, paths.IndexFileName)
	require.NoError(t, os.WriteFile(stale, []byte("sessions: []\n"), 0o644))
	check = doctorCheckLegacyIndex()
	assert.Equal(t, DoctorWarn, check.Status)
	assert.Contains(t, check.Detail, stale)
	assert.Contains(t, check.Detail, migrated)
}

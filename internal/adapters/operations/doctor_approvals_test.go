package operations

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

const approvalsMarker = "DOCTOR-CHECK-APPROVALS-STORE-a2"

func approvalsCheckFor(t *testing.T, appDir string) DoctorCheck {
	t.Helper()
	return doctorCheckApprovalsStore(config.NewFixture(config.Fixture{AppPaths: []string{appDir}}), nil)
}

// An unprovisioned project is the migration case: it withholds everything
// now, so doctor must say so and name the remedy.
func TestDoctorCheckApprovalsStore_UnprovisionedProjectWarnsWithInitRemedy(t *testing.T) {
	t.Cleanup(countersign.SetHomeDirForTesting(t.TempDir()))
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	c := approvalsCheckFor(t, appDir)
	assert.Equal(t, approvalsMarker, c.Marker)
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, "withheld")
	assert.Contains(t, c.Remedy, "ctxloom init")
}

// A store directory without the placeholder is not provisioned: nothing
// tracks it, so a clone of a project with no recorded decision arrives
// without it.
func TestDoctorCheckApprovalsStore_DirectoryWithoutPlaceholderWarns(t *testing.T) {
	t.Cleanup(countersign.SetHomeDirForTesting(t.TempDir()))
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(paths.ApprovalsPath(appDir), 0o755))

	c := approvalsCheckFor(t, appDir)
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, paths.ApprovalsPlaceholderName)
	assert.Contains(t, c.Remedy, "ctxloom init")
}

func TestDoctorCheckApprovalsStore_ProvisionedIsOK(t *testing.T) {
	t.Cleanup(countersign.SetHomeDirForTesting(t.TempDir()))
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, ProvisionApprovalsStore(afero.NewOsFs(), appDir))

	assert.Equal(t, DoctorOK, approvalsCheckFor(t, appDir).Status)
}

// Outside a project the generation reads over the home fallback, whose store
// IS the user store and needs no provisioning — and "run ctxloom init" there
// would scaffold a project in whatever directory the user happens to be in.
func TestDoctorCheckApprovalsStore_HomeFallbackIsInfo(t *testing.T) {
	home := filepath.Join(t.TempDir(), ".ctxloom")
	t.Cleanup(countersign.SetHomeDirForTesting(paths.ApprovalsPath(home)))

	c := approvalsCheckFor(t, home)
	assert.Equal(t, DoctorInfo, c.Status)
	assert.Empty(t, c.Remedy)
}

func TestDoctorCheckApprovalsStore_ConfigErrorWarns(t *testing.T) {
	c := doctorCheckApprovalsStore(nil, errors.New("boom"))
	assert.Equal(t, DoctorWarn, c.Status)
	assert.Contains(t, c.Detail, "boom")
}

package operations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// TestInitializeProject_ProvisionsTheApprovalsStore: init creates the project
// approvals store with its tracked placeholder, so the store resolves READABLE
// from the first command — and its later absence can only mean it went away.
func TestInitializeProject_ProvisionsTheApprovalsStore(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"

	_, err := InitializeProject(context.Background(), engines.Registry(), InitializeProjectRequest{AppDir: appDir, Engine: "claude-code", FS: fs})
	require.NoError(t, err)

	placeholder, err := afero.Exists(fs, filepath.Join(paths.ApprovalsPath(appDir), paths.ApprovalsPlaceholderName))
	require.NoError(t, err)
	assert.True(t, placeholder, "the placeholder is what keeps the store in version control")
	state, err := countersign.NewStore(paths.ApprovalsPath(appDir), fs).Resolve()
	require.NoError(t, err)
	assert.Equal(t, countersign.StateReadable, state)
}

// TestProvisionApprovalsStore_IsIdempotentAndKeepsRecords: the migration path
// runs this over projects that already hold decisions; it must add only what
// is missing and never touch a record.
func TestProvisionApprovalsStore_IsIdempotentAndKeepsRecords(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	record := filepath.Join(paths.ApprovalsPath(appDir), "abc.approve.k.sig")
	require.NoError(t, fs.MkdirAll(paths.ApprovalsPath(appDir), 0o755))
	require.NoError(t, afero.WriteFile(fs, record, []byte("kept"), 0o644))

	require.NoError(t, ProvisionApprovalsStore(fs, appDir))
	require.NoError(t, ProvisionApprovalsStore(fs, appDir))

	data, err := afero.ReadFile(fs, record)
	require.NoError(t, err)
	assert.Equal(t, "kept", string(data))
	assert.True(t, ApprovalsStoreProvisioned(fs, appDir))
}

// TestApprovalsStoreProvisioned_RequiresThePlaceholder: a bare directory is
// NOT provisioned — without the tracked placeholder an empty store is not
// committed, so a fresh clone arrives without it and denies.
func TestApprovalsStoreProvisioned_RequiresThePlaceholder(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	assert.False(t, ApprovalsStoreProvisioned(fs, appDir), "absent")
	require.NoError(t, fs.MkdirAll(paths.ApprovalsPath(appDir), 0o755))
	assert.False(t, ApprovalsStoreProvisioned(fs, appDir), "directory without placeholder")
	require.NoError(t, ProvisionApprovalsStore(fs, appDir))
	assert.True(t, ApprovalsStoreProvisioned(fs, appDir))
}

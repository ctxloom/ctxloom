package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/ident"
	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestRemotesSaveIsWriteBackForm: remotes.yaml is saved in the encoding an
// upgrade write-back would give it.
func TestRemotesSaveIsWriteBackForm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.yaml")
	reg, err := NewRegistry(path)
	require.NoError(t, err)
	require.NoError(t, reg.Add("origin", "https://github.com/example/bundles"))

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	yamlform.RequireWriteBackForm(t, saved)
}

// TestLockfileSaveIsWriteBackForm: lock.yaml is saved in the encoding an
// upgrade write-back would give it.
func TestLockfileSaveIsWriteBackForm(t *testing.T) {
	mgr := NewLockfileManager(t.TempDir())
	require.NoError(t, mgr.Save(&Lockfile{Version: 1, Bundles: map[ident.BundleKey]LockEntry{
		"ctxloom+git://example.test/r//bundles/seed": {SHA: "0000000000000000000000000000000000000000", URL: "u"},
	}}))

	saved, err := os.ReadFile(mgr.Path())
	require.NoError(t, err)
	yamlform.RequireWriteBackForm(t, saved)
}

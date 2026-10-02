package sessions

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestSidecar_IsPrivateToTheOwner: session.yaml carries the engine-to-runner
// MCP bearer (Entry.MCP.Credential), so neither the file nor the session
// directory may be readable by another local user. The directory usually
// exists BEFORE the first sidecar write (launch creates persist/ and friends
// under it), so the test pre-creates it world-readable: tightening only on
// creation would leave the common path open.
func TestSidecar_IsPrivateToTheOwner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	requireIsolatedSessionRoot(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/proj", "mock")
	require.NoError(t, err)

	dir := filepath.Join(m.Root(), e.HarpName)
	require.NoError(t, os.Chmod(dir, 0o755))
	require.NoError(t, os.Chmod(filepath.Join(dir, paths.SessionSidecarFileName), 0o644))

	require.NoError(t, m.BindMCP(e.HarpName, Endpoint{URL: "http://127.0.0.1:1/mcp", Credential: "bearer-1"}))

	info, err := os.Stat(filepath.Join(dir, paths.SessionSidecarFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "session.yaml holds a bearer credential")
	dinfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dinfo.Mode().Perm(), "the session directory is private to its owner")
}

// TestSidecar_NewSessionDirIsPrivate: a session whose directory the sidecar
// write creates gets the private modes from the start.
func TestSidecar_NewSessionDirIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	requireIsolatedSessionRoot(t)
	m, err := Open(nil)
	require.NoError(t, err)
	e, err := m.AssignHarp("/proj", "mock")
	require.NoError(t, err)

	dir := filepath.Join(m.Root(), e.HarpName)
	info, err := os.Stat(filepath.Join(dir, paths.SessionSidecarFileName))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	dinfo, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dinfo.Mode().Perm())
}

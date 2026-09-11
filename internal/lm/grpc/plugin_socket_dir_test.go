package grpc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sunPathLimit is Linux's sun_path capacity; a socket path this long or
// longer fails bind with EINVAL. The production headroom sits below it on
// purpose (macOS is 104), so this is the ceiling the tests hold it against.
const sunPathLimit = 108

// longTempDir builds a TMPDIR the shape an agent cell hands out — deep
// enough that $TMPDIR/pluginNNNNNNNNNN cannot fit in sun_path.
func longTempDir(t *testing.T) string {
	dir := filepath.Join(t.TempDir(), strings.Repeat("cell-scratch-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.Greater(t, len(dir)+pluginSocketNameMax, sunPathLimit,
		"fixture must be long enough to reproduce the defect")
	return dir
}

// shortDir mints a directory whose worst-case socket path fits sun_path.
// It cannot come from t.TempDir(): inside an agent cell that root is the
// very TMPDIR under test, ~100 bytes before the test's own name is added.
func shortDir(t *testing.T) string {
	dir, err := os.MkdirTemp("/tmp", "sock")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.Less(t, len(dir)+pluginSocketNameMax, sunPathLimit, "fixture must be short")
	return dir
}

// The defect: an agent cell's TMPDIR is so deep that go-plugin's default
// socket location exceeds sun_path. The fix has to steer the plugin to a
// directory whose worst-case socket path still fits.
func TestPluginSocketDir_LongTMPDIR_FitsSunPath(t *testing.T) {
	t.Setenv("TMPDIR", longTempDir(t))
	t.Setenv("XDG_RUNTIME_DIR", "")

	dir := pluginSocketDir()

	require.NotEmpty(t, dir, "a long TMPDIR must be steered away from, not inherited")
	assert.Less(t, len(dir)+pluginSocketNameMax, sunPathLimit,
		"socket path %s/plugin0000000000 must fit sun_path", dir)
	assert.NotEqual(t, os.TempDir(), dir)
}

func TestPluginSocketDir_PrefersRuntimeDirCtxloom(t *testing.T) {
	xdg := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	t.Setenv("TMPDIR", longTempDir(t))

	dir := pluginSocketDir()

	assert.Equal(t, filepath.Join(xdg, "ctxloom"), dir,
		"the runner's own host-tier directory is the preferred socket home")
	info, err := os.Stat(dir)
	require.NoError(t, err, "the directory must be created, not merely named")
	assert.True(t, info.IsDir())
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "user-private, like the runner's tier")
}

func TestPluginSocketDir_ShortTMPDIRWithoutRuntimeDir_KeepsTempDir(t *testing.T) {
	short := shortDir(t)
	t.Setenv("TMPDIR", short)
	t.Setenv("XDG_RUNTIME_DIR", "")

	assert.Equal(t, short, pluginSocketDir(),
		"a TMPDIR that fits is left alone: go-plugin's default location is only overridden when it cannot work")
}

func TestPluginSocketDir_OverlongRuntimeDirIsSkipped(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", longTempDir(t))
	short := shortDir(t)
	t.Setenv("TMPDIR", short)

	assert.Equal(t, short, pluginSocketDir(),
		"a runtime dir that itself blows sun_path falls through to the next tier")
}

func TestPluginSpawnEnv_StampsSocketDirOntoSpawnEnv(t *testing.T) {
	xdg := shortDir(t)
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	t.Setenv("TMPDIR", longTempDir(t))
	t.Setenv(plugin.EnvUnixSocketDir, "")
	require.NoError(t, os.Unsetenv(plugin.EnvUnixSocketDir))

	got := pluginSpawnEnv([]string{"CTXLOOM_X=1"})

	assert.Equal(t, []string{"CTXLOOM_X=1", plugin.EnvUnixSocketDir + "=" + filepath.Join(xdg, "ctxloom")}, got,
		"the caller's per-spawn env is preserved and the socket dir rides after it")
}

func TestPluginSpawnEnv_AmbientSocketDirIsRespected(t *testing.T) {
	t.Setenv(plugin.EnvUnixSocketDir, "/somewhere/operator/chose")
	t.Setenv("XDG_RUNTIME_DIR", shortDir(t))

	got := pluginSpawnEnv([]string{"CTXLOOM_X=1"})

	assert.Equal(t, []string{"CTXLOOM_X=1"}, got,
		"an operator's own PLUGIN_UNIX_SOCKET_DIR is inherited by the child, not shadowed")
}

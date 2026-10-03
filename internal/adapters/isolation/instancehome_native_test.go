package isolation

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// nativeLayout is a session dir's home/claude and native/claude, as
// launch.SessionHome and launch.NativeHome place them.
func nativeLayout(t *testing.T) (instance, native string) {
	t.Helper()
	sd := t.TempDir()
	return filepath.Join(sd, paths.SessionEngineHomesDirName, claude.HomeLeaf), filepath.Join(sd, paths.NativeDirName, claude.HomeLeaf)
}

// The session home's history dir is a RELATIVE link into native/, so the one
// link resolves on the host and in a container that mounts the two as
// siblings, and deleting the home leaves the history.
func TestPrepareInstanceHome_LinksTheHistoryStoreIntoNative(t *testing.T) {
	withFakeHome(t)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{report: engine.InstanceConfigReport{}})
	instance, native := nativeLayout(t)

	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, NativeHome: native})
	require.NoError(t, err)

	link := filepath.Join(instance, claude.TranscriptsDirName)
	st, err := os.Lstat(link)
	require.NoError(t, err)
	require.NotZero(t, st.Mode()&fs.ModeSymlink, "projects/ is a link, never a real dir in the disposable home")
	got, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("..", "..", paths.NativeDirName, claude.HomeLeaf, claude.TranscriptsDirName), got)

	written := filepath.Join(link, "-proj", "s.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(written), 0o700))
	require.NoError(t, os.WriteFile(written, []byte("{}\n"), 0o600))
	require.NoError(t, os.RemoveAll(instance))
	_, err = os.Stat(filepath.Join(native, claude.TranscriptsDirName, "-proj", "s.jsonl"))
	assert.NoError(t, err, "the history outlives the home")

	_, err = PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, NativeHome: native})
	require.NoError(t, err, "a rebuilt home is linked again, to the history already there")
	_, err = os.Stat(written)
	assert.NoError(t, err)
}

// A real directory where the link belongs means history written there dies
// with the home: refused, never adopted or moved.
func TestPrepareInstanceHome_RefusesARealHistoryDirInTheHome(t *testing.T) {
	withFakeHome(t)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{report: engine.InstanceConfigReport{}})
	instance, native := nativeLayout(t)
	require.NoError(t, os.MkdirAll(filepath.Join(instance, claude.TranscriptsDirName), 0o700))

	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, NativeHome: native})
	assert.ErrorIs(t, err, ErrHistoryNotLinked)
}

// No native home (a harpless run, or an engine with no history store): the
// home is prepared and nothing is linked.
func TestPrepareInstanceHome_WithoutANativeHomeLinksNothing(t *testing.T) {
	withFakeHome(t)
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{report: engine.InstanceConfigReport{}})
	instance, _ := nativeLayout(t)
	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance})
	require.NoError(t, err)
	_, err = os.Lstat(filepath.Join(instance, claude.TranscriptsDirName))
	assert.True(t, os.IsNotExist(err))
}

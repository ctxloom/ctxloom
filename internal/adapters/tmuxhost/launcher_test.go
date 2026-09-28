//go:build !windows

package tmuxhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const launcherSecret = "sk-ant-oat01-LAUNCHER-SENTINEL"

// A launcher RUN by sh sees its environment, and no byte of it — a
// credential included — is left on disk: not in the script, not in the
// directory once the script has read it.
func TestWriteLauncher_TheEnvironmentReachesTheScriptAndNeverTheDisk(t *testing.T) {
	dir := t.TempDir()
	l, err := writeLauncher(dir, "x", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": launcherSecret}, "sh", []string{"-c", `printf %s "$CLAUDE_CODE_OAUTH_TOKEN"`})
	require.NoError(t, err)
	out, err := exec.Command("sh", l.path).Output()
	require.NoError(t, err)
	assert.Equal(t, launcherSecret, string(out), "the environment reaches the hosted program")
	<-l.feed.done
	requireNoSentinelUnder(t, dir)
	_, err = os.Stat(filepath.Join(dir, "ctxloom-env-x.fifo"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the channel is unlinked once read")
}

// A feed nobody reads (the window never ran) is released, not leaked, and
// leaves nothing behind.
func TestWriteLauncher_AnUnreadFeedIsReleased(t *testing.T) {
	dir := t.TempDir()
	l, err := writeLauncher(dir, "y", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": launcherSecret}, "true", nil)
	require.NoError(t, err)
	l.feed.release()
	<-l.feed.done
	requireNoSentinelUnder(t, dir)
	_, err = os.Stat(filepath.Join(dir, "ctxloom-env-y.fifo"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func requireNoSentinelUnder(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&os.ModeNamedPipe != 0 {
			return err
		}
		b, rerr := os.ReadFile(p)
		require.NoError(t, rerr)
		assert.NotContains(t, string(b), launcherSecret, "%s holds the credential", p)
		return nil
	}))
}

//go:build !windows

package tmuxhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const launcherSecret = "sk-ant-oat01-LAUNCHER-SENTINEL"

// feedBound caps every wait on a launch's FIFO. Both ends of a FIFO block
// until the other end shows up, so a feed that never writes, never closes or
// is never released parks its reader or its writer for good; the bound turns
// that into a failure the test reports.
const feedBound = 5 * time.Second

// awaitFeed waits, bounded, for the feed's writer to finish.
func awaitFeed(t *testing.T, f *envFeed) {
	t.Helper()
	select {
	case <-f.done:
	case <-time.After(feedBound):
		t.Fatalf("the environment feed's writer never finished (%s)", f.fifo)
	}
}

// A launcher RUN by sh sees its environment, and no byte of it — a
// credential included — is left on disk: not in the script, not in the
// directory once the script has read it.
func TestWriteLauncher_TheEnvironmentReachesTheScriptAndNeverTheDisk(t *testing.T) {
	dir := t.TempDir()
	l, err := writeLauncher(dir, "x", map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": launcherSecret}, "sh", []string{"-c", `printf %s "$CLAUDE_CODE_OAUTH_TOKEN"`})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), feedBound)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", l.path).Output()
	require.NoError(t, err, "the script must read its environment and exit (ctx: %v)", ctx.Err())
	assert.Equal(t, launcherSecret, string(out), "the environment reaches the hosted program")
	awaitFeed(t, l.feed)
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
	awaitFeed(t, l.feed)
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

package isolation

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The seeded credential must SURVIVE a host token refresh.
//
// Claude's OAuth refresh rotates the token and REVOKES the old one, and the
// host's claude lands the new one by write-by-rename over
// ~/.claude/.credentials.json. Every instance holding the old bytes is dead
// on its next request. Two things therefore have to be true of a controlled
// home, and these tests pin each:
//
//   - preparing the home AGAIN (a resumed run) replaces whatever copy an
//     earlier run left behind with the host's current material;
//   - while the run lives, a host refresh reaches the instance, and once the
//     run ends nothing goes on writing into it.

// rotateByRename replaces path's content the way claude's own refresh does:
// a sibling temp file renamed over the name. A directory watch bound to the
// NAME hears this; one bound to the old inode would not.
func rotateByRename(t *testing.T, path string, content []byte) {
	t.Helper()
	tmp := path + ".tmp"
	require.NoError(t, os.WriteFile(tmp, content, 0o600))
	require.NoError(t, os.Rename(tmp, path))
}

// seededClaudeHome stands up a fake host home with a claude credential and
// declares a no-op instance-config writer, so CopyAmbient's only observable
// work is the credential placement.
func seededClaudeHome(t *testing.T, hostBytes []byte) (hostFile string) {
	t.Helper()
	testsupport.Isolate(t)
	home := withFakeHome(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o700))
	hostFile = filepath.Join(home, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(hostFile, hostBytes, 0o600))
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{})
	return hostFile
}

// A RESUMED run prepares the same instance a dead run left behind — holding
// the token that was current when THAT run was seeded, which the host has
// since rotated and the server has since revoked. Preparing the home again
// must hand the instance the host's current bytes, as a real owner-only file
// (claude refuses a symlinked credential at the syscall).
func TestCopyAmbient_ResumeReplacesAStaleInstanceCredential(t *testing.T) {
	seededClaudeHome(t, []byte(`{"token":"rotated-by-the-host"}`))

	instance := t.TempDir()
	stale := filepath.Join(instance, "claude", ".credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o700))
	// Looser than owner-only on purpose: a copy an earlier build left at a
	// wider mode must not keep that mode once live bytes are placed in it.
	require.NoError(t, os.WriteFile(stale, []byte(`{"token":"revoked"}`), 0o644))

	_, err := copyAmbient(t, AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)

	got, err := os.ReadFile(stale)
	require.NoError(t, err)
	assert.JSONEq(t, `{"token":"rotated-by-the-host"}`, string(got),
		"a resumed run must present the host's CURRENT token, not the one its last run was seeded with")
	info, err := os.Lstat(stale)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular(), "claude refuses a symlinked credential; the instance file must be a real file")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// While the run lives, the instance FOLLOWS the host file: a refresh the host
// lands by rename reaches the instance; a host that does not change leaves
// the instance untouched; and closing the report ends the following, so a
// finished run leaves no watcher behind writing into a home nothing reads.
func TestCopyAmbient_InstanceFollowsTheHostCredentialUntilClosed(t *testing.T) {
	hostFile := seededClaudeHome(t, []byte(`{"token":"one"}`))

	instance := t.TempDir()
	report, err := copyAmbient(t, AmbientRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	require.NoError(t, err)
	instFile := filepath.Join(instance, "claude", ".credentials.json")
	eventuallyReads(t, instFile, `{"token":"one"}`)

	rotateByRename(t, hostFile, []byte(`{"token":"two"}`))
	eventuallyReads(t, instFile, `{"token":"two"}`)
	info, err := os.Stat(instFile)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a refreshed copy is still owner-only")

	// Unchanged host: the instance must not be rewritten. Several debounce
	// windows is long enough for a replicator that was going to write to
	// have written.
	before, err := os.Stat(instFile)
	require.NoError(t, err)
	time.Sleep(4 * replicationDebounce)
	after, err := os.Stat(instFile)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(), "an unchanged host file must not be rewritten into the instance")

	require.NoError(t, report.Close())
	rotateByRename(t, hostFile, []byte(`{"token":"three-after-the-run-ended"}`))
	time.Sleep(4 * replicationDebounce)
	got, err := os.ReadFile(instFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"token":"two"}`, string(got),
		"after Close nothing may go on writing into the instance: the following ends with the run")
}

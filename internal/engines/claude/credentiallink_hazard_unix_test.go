//go:build !windows

package claude

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// WHY THIS FILE EXISTS.
//
// ctxloom places no credential file in a claude agent's home: the run
// authenticates from the setup-token in CLAUDE_CODE_OAUTH_TOKEN (see
// Claude.Home and TestHome_DeclaresTokenAuth). The obvious alternative, one
// that would let a token refresh by any agent reach the human and every other
// agent, is to LINK the agent's credential to the human's real one. It
// does not work for this engine, and these tests are the measured reason.
// They exist so that a future link or mount of the real credential is not
// attempted without re-running them against the shape being proposed.
//
// The structural arms measure the two write styles a refreshing program may
// use against each link shape. What makes them decisive is how claude
// itself behaves:
//
//   - it opens its credential with O_NOFOLLOW and maps the resulting ELOOP
//     to a "refused-symlink" state, so a symlinked credential never
//     authenticates at all. Only the kernel half of that is pinned here
//     (TestCredentialSymlink_IsRefusedByAnONOFOLLOWReader); the ELOOP
//     mapping lives in claude's shipped code, and no symbol in this
//     repository encodes it.
//   - it writes the credential by staging a temp file and renaming it over
//     the target, which orphans a hardlink. Its fallback to an in-place write
//     on a small errno set (EXDEV, EBUSY among them) is what a bind MOUNT of
//     the file would depend on. The only check of that writer is the
//     tag-gated probe TestClaudeCredentialWriter_FallsBackThroughEBUSY, which
//     runs against the installed binary, not the default suite.
//
// Linking the containing DIRECTORY is the one shape that survives a rename,
// and it is unavailable: the credential lives in the directory
// CLAUDE_CONFIG_DIR names (HomeLeaf), and the per-session instance config is
// written into that same directory. Linking it to the human's real config
// dir would write every session's config into the human's engine home.
//
// Every test works in temp dirs. Nothing reads or writes the real home;
// "host" below is always a fixture directory.

const (
	hazardOriginal  = `{"token":"original"}`
	hazardRefreshed = `{"token":"refreshed"}`
)

// writeByRename refreshes target the way a program seeking atomicity does:
// write a sibling temp file, then rename it over the target. This is the
// style that breaks links, because rename REPLACES the directory entry.
func writeByRename(t *testing.T, target, data string) {
	t.Helper()
	tmp := target + ".tmp"
	require.NoError(t, os.WriteFile(tmp, []byte(data), 0o600))
	require.NoError(t, os.Rename(tmp, target))
}

// writeInPlace refreshes target by opening and truncating the existing path,
// so the write lands on whatever inode the path currently resolves to.
func writeInPlace(t *testing.T, target, data string) {
	t.Helper()
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	_, werr := f.WriteString(data)
	require.NoError(t, werr)
	require.NoError(t, f.Close())
}

// hazardFixture lays down a host credential and returns the host path plus an
// agent directory to link from.
func hazardFixture(t *testing.T) (hostCred, agentDir string) {
	t.Helper()
	hostDir := t.TempDir()
	hostCred = filepath.Join(hostDir, ".credentials.json")
	require.NoError(t, os.WriteFile(hostCred, []byte(hazardOriginal), 0o600))
	return hostCred, t.TempDir()
}

func readHazardFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(b)
}

func inodeOf(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	require.NoError(t, err)
	st, ok := fi.Sys().(*syscall.Stat_t)
	require.True(t, ok, "need the raw stat to compare inodes")
	return uint64(st.Ino)
}

// TestCredentialFileSymlink_SurvivesAnInPlaceRefresh is the ONLY arm in which
// a file-level link carries a refresh back to the host. An in-place write
// follows the symlink and lands on the host's own inode.
//
// It proves the failures below are caused by the WRITE STYLE and not by the
// linking itself. Without it, a file where every case fails cannot
// distinguish "links do not work" from "the fixture is broken".
func TestCredentialFileSymlink_SurvivesAnInPlaceRefresh(t *testing.T) {
	hostCred, agentDir := hazardFixture(t)
	linked := filepath.Join(agentDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	writeInPlace(t, linked, hazardRefreshed)

	assert.Equal(t, hazardRefreshed, readHazardFile(t, hostCred),
		"an in-place refresh through a symlink must reach the host credential")
}

// TestCredentialFileSymlink_BreaksSilentlyOnAWriteByRenameRefresh is the
// hazard, measured. A rename over the link REPLACES the symlink with a
// regular file: the agent now has its own private credential, the host still
// has the stale one, and NOTHING reports it.
func TestCredentialFileSymlink_BreaksSilentlyOnAWriteByRenameRefresh(t *testing.T) {
	hostCred, agentDir := hazardFixture(t)
	linked := filepath.Join(agentDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	writeByRename(t, linked, hazardRefreshed)

	assert.Equal(t, hazardOriginal, readHazardFile(t, hostCred),
		"the host credential is STALE: the refresh never reached it")
	assert.Equal(t, hazardRefreshed, readHazardFile(t, linked),
		"the agent kept the refresh to itself")

	fi, err := os.Lstat(linked)
	require.NoError(t, err)
	assert.Zero(t, fi.Mode()&os.ModeSymlink,
		"the link is GONE — rename replaced it with a regular file, and nothing said so")
}

// TestCredentialFileHardlink_BreaksIdenticallyOnAWriteByRenameRefresh: the
// hardlink fares no better. Rename installs a NEW inode at the path, so the
// host's inode keeps the stale token.
//
// A hardlink additionally cannot cross filesystems, and a project's state
// directory and the user's home routinely are on different ones, so this
// shape can fail at CREATION as well as at refresh.
func TestCredentialFileHardlink_BreaksIdenticallyOnAWriteByRenameRefresh(t *testing.T) {
	hostCred, agentDir := hazardFixture(t)
	linked := filepath.Join(agentDir, ".credentials.json")
	if err := os.Link(hostCred, linked); err != nil {
		t.Skipf("hardlink across these temp dirs is unavailable (%v) — itself an instance of the cross-filesystem limit", err)
	}
	require.Equal(t, inodeOf(t, hostCred), inodeOf(t, linked), "the hardlink starts as one inode")

	writeByRename(t, linked, hazardRefreshed)

	assert.Equal(t, hazardOriginal, readHazardFile(t, hostCred),
		"the host credential is STALE: the refresh never reached it")
	assert.NotEqual(t, inodeOf(t, hostCred), inodeOf(t, linked),
		"rename gave the agent a NEW inode — the hardlink is orphaned")
}

// TestCredentialDirectorySymlink_SurvivesAWriteByRenameRefresh records the one
// shape that DOES carry a rename-style refresh: link the containing
// DIRECTORY, so a rename inside it lands inside the real directory. It is
// measured to make the finding complete, not because it is available to this
// engine; the file header says why it is not.
func TestCredentialDirectorySymlink_SurvivesAWriteByRenameRefresh(t *testing.T) {
	hostDir := t.TempDir()
	hostCred := filepath.Join(hostDir, ".credentials.json")
	require.NoError(t, os.WriteFile(hostCred, []byte(hazardOriginal), 0o600))

	linkedDir := filepath.Join(t.TempDir(), HomeLeaf)
	require.NoError(t, os.Symlink(hostDir, linkedDir))

	writeByRename(t, filepath.Join(linkedDir, ".credentials.json"), hazardRefreshed)

	assert.Equal(t, hazardRefreshed, readHazardFile(t, hostCred),
		"a rename INSIDE a symlinked directory lands on the real directory, so the refresh propagates")
}

// TestCredentialSymlink_IsRefusedByAnONOFOLLOWReader pins the kernel
// behaviour claude relies on to refuse a symlinked credential: an O_NOFOLLOW
// open of a symlink fails with ELOOP. A symlinked credential therefore does
// not degrade on the first refresh; it fails to authenticate on every run.
func TestCredentialSymlink_IsRefusedByAnONOFOLLOWReader(t *testing.T) {
	hostCred, agentDir := hazardFixture(t)
	linked := filepath.Join(agentDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	fd, err := syscall.Open(linked, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		_ = syscall.Close(fd)
	}
	require.Error(t, err, "an O_NOFOLLOW open of a symlink must fail")
	assert.Equal(t, syscall.ELOOP, err,
		"the failure is ELOOP — the errno claude turns into refused-symlink")

	// The same open against a REGULAR file succeeds, so the refusal is about
	// the link shape, not the path.
	fd2, err2 := syscall.Open(hostCred, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	require.NoError(t, err2, "a regular credential file opens fine under O_NOFOLLOW")
	_ = syscall.Close(fd2)
}

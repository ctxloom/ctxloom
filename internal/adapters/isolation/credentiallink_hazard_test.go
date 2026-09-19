package isolation

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
	claudeengine "github.com/ctxloom/ctxloom/internal/engines/claude/engine"
)

// WHY THIS FILE EXISTS.
//
// The controlled home seeds the instance with a COPY of the host credential
// (credentialseed.go). A copy means a token refresh inside one instance stays
// in that instance: other agents, and the host, never see it. The obvious
// remedy is to LINK the instance's credential to the host's so a refresh by
// any agent is a refresh for all.
//
// These tests measure whether a link can actually carry that invariant. They
// are structural — they exercise the two write styles a refreshing program
// may use against each link shape — plus two pins on the facts about THIS
// engine that decide which shapes are even available. The answer is recorded
// as executable assertions rather than prose so that a future attempt to
// switch the seed to a link finds the reason it was not, and can re-run it.
//
// Every test here works in temp dirs. Nothing reads or writes the real host
// home; "host" below is always a fixture directory.

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
// instance directory to link from.
func hazardFixture(t *testing.T) (hostCred, instanceDir string) {
	t.Helper()
	hostDir := t.TempDir()
	hostCred = filepath.Join(hostDir, ".credentials.json")
	require.NoError(t, os.WriteFile(hostCred, []byte(hazardOriginal), 0o600))
	return hostCred, t.TempDir()
}

func readFile(t *testing.T, path string) string {
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
// It is here to prove the failures below are caused by the WRITE STYLE and
// not by the linking itself — without this arm, a test file where every case
// fails cannot distinguish "links do not work" from "the fixture is broken".
func TestCredentialFileSymlink_SurvivesAnInPlaceRefresh(t *testing.T) {
	hostCred, instanceDir := hazardFixture(t)
	linked := filepath.Join(instanceDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	writeInPlace(t, linked, hazardRefreshed)

	assert.Equal(t, hazardRefreshed, readFile(t, hostCred),
		"an in-place refresh through a symlink must reach the host credential")
}

// TestCredentialFileSymlink_BreaksSilentlyOnAWriteByRenameRefresh is the
// hazard, measured. A rename over the link REPLACES the symlink with a
// regular file: the instance now has its own private credential, the host
// still has the stale one, and NOTHING reports it. Propagation dies on the
// first refresh.
//
// MUTATION TARGET — make the seed create a symlink and keep this assertion,
// and the shipped behaviour is pinned as broken-on-first-refresh.
func TestCredentialFileSymlink_BreaksSilentlyOnAWriteByRenameRefresh(t *testing.T) {
	hostCred, instanceDir := hazardFixture(t)
	linked := filepath.Join(instanceDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	writeByRename(t, linked, hazardRefreshed)

	assert.Equal(t, hazardOriginal, readFile(t, hostCred),
		"the host credential is STALE: the refresh never reached it")
	assert.Equal(t, hazardRefreshed, readFile(t, linked),
		"the instance kept the refresh to itself")

	fi, err := os.Lstat(linked)
	require.NoError(t, err)
	assert.Zero(t, fi.Mode()&os.ModeSymlink,
		"the link is GONE — rename replaced it with a regular file, and nothing said so")
}

// TestCredentialFileHardlink_BreaksIdenticallyOnAWriteByRenameRefresh: the
// hardlink fares no better. Rename installs a NEW inode at the path, so the
// host's inode — still perfectly valid, still hardlinked to nothing the
// instance uses — keeps the stale token.
//
// A hardlink additionally cannot cross filesystems, and the instance home
// (<project>/.ctxloom/state/) and the user's home routinely are on different
// ones, so this shape can fail at CREATION as well as at refresh.
func TestCredentialFileHardlink_BreaksIdenticallyOnAWriteByRenameRefresh(t *testing.T) {
	hostCred, instanceDir := hazardFixture(t)
	linked := filepath.Join(instanceDir, ".credentials.json")
	if err := os.Link(hostCred, linked); err != nil {
		t.Skipf("hardlink across these temp dirs is unavailable (%v) — itself an instance of the cross-filesystem limit", err)
	}
	require.Equal(t, inodeOf(t, hostCred), inodeOf(t, linked), "the hardlink starts as one inode")

	writeByRename(t, linked, hazardRefreshed)

	assert.Equal(t, hazardOriginal, readFile(t, hostCred),
		"the host credential is STALE: the refresh never reached it")
	assert.NotEqual(t, inodeOf(t, hostCred), inodeOf(t, linked),
		"rename gave the instance a NEW inode — the hardlink is orphaned")
}

// TestCredentialDirectorySymlink_SurvivesAWriteByRenameRefresh records the one
// shape that DOES carry a rename-style refresh: link the containing
// DIRECTORY, so a rename inside it lands inside the real directory.
//
// It is measured here to make the finding complete, NOT because it is
// available to this engine — see
// TestClaudeCredentialSharesItsLeafWithTheInstanceConfig for why adopting it
// would defeat the isolation the controlled home exists to provide.
func TestCredentialDirectorySymlink_SurvivesAWriteByRenameRefresh(t *testing.T) {
	hostDir := t.TempDir()
	hostCred := filepath.Join(hostDir, ".credentials.json")
	require.NoError(t, os.WriteFile(hostCred, []byte(hazardOriginal), 0o600))

	linkedDir := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.Symlink(hostDir, linkedDir))

	writeByRename(t, filepath.Join(linkedDir, ".credentials.json"), hazardRefreshed)

	assert.Equal(t, hazardRefreshed, readFile(t, hostCred),
		"a rename INSIDE a symlinked directory lands on the real directory, so the refresh propagates")
}

// TestCredentialSymlink_IsRefusedByAnONOFOLLOWReader pins why the symlink
// shape is not merely fragile for claude but unusable from the first read.
//
// claude-code opens its credential with O_NOFOLLOW and maps the resulting
// ELOOP to an explicit "refused-symlink" state — it declines to read a
// credential that is a symlink at all. Seeding a symlink would therefore not
// degrade on the first refresh; it would fail to authenticate immediately,
// on every run. This asserts the kernel behaviour that engine relies on.
func TestCredentialSymlink_IsRefusedByAnONOFOLLOWReader(t *testing.T) {
	hostCred, instanceDir := hazardFixture(t)
	linked := filepath.Join(instanceDir, ".credentials.json")
	require.NoError(t, os.Symlink(hostCred, linked))

	fd, err := syscall.Open(linked, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err == nil {
		_ = syscall.Close(fd)
	}
	require.Error(t, err, "an O_NOFOLLOW open of a symlink must fail")
	assert.Equal(t, syscall.ELOOP, err,
		"the failure is ELOOP — the errno claude-code turns into refused-symlink")

	// The same open against a REGULAR file (what the copy seeds today)
	// succeeds, so the refusal is about the link shape, not the path.
	fd2, err2 := syscall.Open(hostCred, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0)
	require.NoError(t, err2, "a regular credential file opens fine under O_NOFOLLOW")
	_ = syscall.Close(fd2)
}

// TestClaudeCredentialSharesItsLeafWithTheInstanceConfig closes the last
// escape route from the engine's OWN declarations: the credential seed and
// the relocatable config home name the SAME leaf. The credential lives at
// <instance>/claude/.credentials.json and the per-session instance config is
// written into that very <instance>/claude/ directory.
//
// So the directory-symlink shape — the only one that survives a rename — is
// unavailable here: symlinking that leaf at the user's real ~/.claude would
// send every session's generated instance config into the human's own engine
// home, destroying both the isolation the controlled home exists to create
// and the user's real configuration. That is a worse outcome than the
// stale-token bug linking was meant to fix.
//
// Pinned from the declarations rather than asserted in prose so that an
// engine which later SPLITS the two leaves makes this test fail and reopens
// the option deliberately.
func TestClaudeCredentialSharesItsLeafWithTheInstanceConfig(t *testing.T) {
	desc := claudeengine.Descriptor()

	home, ok := desc.Home.Get()
	require.True(t, ok, "claude declares an engine home")

	seed, ok := home.Credentials.Get()
	require.True(t, ok, "claude declares a credential seed")

	require.NotEmpty(t, home.Vars, "claude declares a relocatable config home")
	homeLeaf := home.Vars[0].Subdir

	assert.Equal(t, claude.HomeLeaf, homeLeaf)
	assert.Equal(t, homeLeaf, seed.Subdir,
		"the credential seed and the config home share ONE leaf, so the credential's directory cannot be linked away without taking the instance config with it")
}

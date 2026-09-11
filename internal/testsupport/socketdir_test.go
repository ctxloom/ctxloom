package testsupport

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overlongTier builds a directory the shape an agent cell's TMPDIR takes —
// deep enough that no socket name can fit beneath it.
func overlongTier(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), strings.Repeat("cell-scratch-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.Greater(t, len(dir), sunPathHeadroom, "fixture must be long enough to reproduce the defect")
	return dir
}

func TestSocketDir_MintsADirectoryTheSocketCanBind(t *testing.T) {
	var dir string
	t.Run("inner", func(t *testing.T) {
		dir = SocketDir(t, "agent.sock")

		sock := filepath.Join(dir, "agent.sock")
		assert.LessOrEqual(t, len(sock), sunPathHeadroom)
		ln, err := net.Listen("unix", sock)
		require.NoError(t, err, "the whole point: the path must actually bind")
		require.NoError(t, ln.Close())
	})
	assert.NoDirExists(t, dir, "the directory is removed when the test that minted it ends")
}

func TestSocketDir_EachTestGetsItsOwnDirectory(t *testing.T) {
	a := SocketDir(t, "a.sock")
	b := SocketDir(t, "a.sock")
	assert.NotEqual(t, a, b)
}

func TestSocketDir_OverlongTierFallsThroughToTheNext(t *testing.T) {
	long := overlongTier(t)

	dir := socketDir(t, "x.sock", []string{long, "/tmp"})

	assert.Equal(t, "/tmp", filepath.Dir(dir), "the first tier that fits is taken")
	entries, err := os.ReadDir(long)
	require.NoError(t, err)
	assert.Empty(t, entries, "a rejected tier keeps nothing behind")
}

func TestSocketDir_PrefersTheRuntimeDirTier(t *testing.T) {
	// Not t.TempDir(): under a long GOTMPDIR that root would itself be
	// rejected as a tier, which is the fall-through case, not this one.
	xdg, err := os.MkdirTemp("/tmp", "xdg")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(xdg) })
	t.Setenv("XDG_RUNTIME_DIR", xdg)

	dir := SocketDir(t, "x.sock")

	assert.Equal(t, filepath.Join(xdg, "ctxloom-test"), filepath.Dir(dir),
		"a ctxloom-test SIBLING of production's socket home, never that directory itself")
	info, err := os.Stat(filepath.Dir(dir))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "user-private, like the production tier")
}

// recordingTB captures Fatalf so the failure text can be asserted on. A real
// *testing.T would end the test at the first Fatalf; the helper's contract
// is that Fatalf is the last thing it does, so recording is enough.
type recordingTB struct {
	testing.TB
	fatal string
}

func (r *recordingTB) Helper()                           {}
func (r *recordingTB) Cleanup(func())                    {}
func (r *recordingTB) Fatalf(format string, args ...any) { r.fatal = fmt.Sprintf(format, args...) }

func TestSocketDir_FailsNamingTheMeasuredLengthNotTheKernelErrno(t *testing.T) {
	long := overlongTier(t)
	rec := &recordingTB{}

	got := socketDir(rec, "agent.sock", []string{long})

	assert.Empty(t, got)
	require.NotEmpty(t, rec.fatal, "an unbindable path must fail the test, not hand the caller a path the kernel will refuse")
	assert.Contains(t, rec.fatal, fmt.Sprintf("%d-byte", sunPathHeadroom), "the budget is named")
	assert.Contains(t, rec.fatal, long, "the rejected path is named")
	m := regexp.MustCompile(`(\d+) bytes`).FindStringSubmatch(rec.fatal)
	require.NotNil(t, m, "the measured length is named: %s", rec.fatal)
	measured, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	assert.Greater(t, measured, sunPathHeadroom, "the measurement is of the path that was actually minted")
	assert.NotContains(t, rec.fatal, "invalid argument", "the diagnosis replaces the errno; it does not repeat it")
}

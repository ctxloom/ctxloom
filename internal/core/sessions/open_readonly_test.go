package sessions

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Opening the store and reading it is not a write: `session list`, the MCP
// session tools and every other reader arrive through Open, so a root created
// there would appear under HOME on the first read-only command. The root
// comes into being when the first session is minted (AssignHarp).
func TestOpen_ReadsLeaveTheSessionsRootUncreated(t *testing.T) {
	testsupport.Isolate(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)

	m, err := Open(nil)
	require.NoError(t, err)
	all, err := m.ListAll()
	require.NoError(t, err)
	require.Empty(t, all)
	mine, err := m.ListForProject("/proj")
	require.NoError(t, err)
	require.Empty(t, mine)
	require.NoDirExists(t, root, "a read through Open must not create the sessions root")

	// The writer still lays out the root it needs.
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.DirExists(t, root)
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
}

// A root that exists but is not a directory is refused at Open with the OS's
// own error, so a caller can tell it apart and report the real reason.
func TestOpen_RefusesARootThatIsNotADirectory(t *testing.T) {
	testsupport.Isolate(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(root), 0o755))
	require.NoError(t, os.WriteFile(root, []byte("not a directory\n"), 0o644))

	_, err = Open(nil)
	require.ErrorIs(t, err, syscall.ENOTDIR)
}

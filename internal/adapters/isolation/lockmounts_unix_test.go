//go:build unix

package isolation

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A host lock path that resolves to anything but a regular file is refused,
// not bound: a FIFO bound as the lock would hand the child no lock at all.
func TestLockMounts_RefusesANonRegularHostLock(t *testing.T) {
	testsupport.Isolate(t)
	projectDir, scratch := t.TempDir(), t.TempDir()
	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	hostLock, err := paths.HomePathFor(filepath.Join(projectDir, c.engineSpec.inPlaceFiles[0]))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(hostLock), 0o755))
	require.NoError(t, syscall.Mkfifo(hostLock, 0o600))

	_, err = c.lockMounts(projectDir, scratch)
	require.ErrorIs(t, err, safefs.ErrNotRegularFile)
}

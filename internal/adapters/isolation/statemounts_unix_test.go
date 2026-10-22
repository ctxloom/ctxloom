//go:build !windows

package isolation

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The project store dir is created 0755, the mode its host-side siblings
// (~/.ctxloom/tasks, the session dirs) get, so the host user's own taskloom
// and every runtime's in-container writer (mapped to that same user) use it
// alike. The umask is cleared for the call, or a masking umask would make
// any requested mode read back the same. syscall.Umask is process-global;
// this package runs no parallel tests.
func TestSessionStateMounts_StoreDirModeIs0755(t *testing.T) {
	home := testsupport.Isolate(t)
	prev := syscall.Umask(0)
	defer syscall.Umask(prev)

	c := NewContainerFor(fakeRuntime{name: "docker", available: true}, "claude-code")
	c.state = SessionState{Harp: "brisk-teal-otter", ProjectID: "proj-1"}
	_, _, err := c.sessionStateMounts()
	require.NoError(t, err)

	info, err := os.Stat(filepath.Join(home, ".ctxloom", "projects", "proj-1"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
}

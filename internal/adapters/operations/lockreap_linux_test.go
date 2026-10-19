//go:build linux

package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The real /proc listing shows this process, named and linked to the binary
// it runs — the entry the gate's non-vacuity check depends on.
func TestListProcesses_ShowsThisProcess(t *testing.T) {
	procs, err := listProcesses()
	require.NoError(t, err)
	exe, err := os.Executable()
	require.NoError(t, err)
	exe, err = filepath.EvalSymlinks(exe)
	require.NoError(t, err)

	for _, p := range procs {
		if p.PID != os.Getpid() {
			continue
		}
		require.NoError(t, p.ExeErr)
		assert.Equal(t, exe, p.Exe)
		comm := filepath.Base(exe)
		if len(comm) > 15 {
			comm = comm[:15]
		}
		assert.Equal(t, comm, p.Comm)
		assert.Greater(t, len(procs), 1, "the listing shows only this process")
		return
	}
	t.Fatalf("the listing of %d processes does not show this one (pid %d)", len(procs), os.Getpid())
}

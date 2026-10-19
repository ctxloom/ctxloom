//go:build windows

package runner

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStartBatchViaCmdExe pins the Windows wiring: a batch engine is started
// as cmd.exe on batchCommandLine's line, any other engine as it is, and an
// argument cmd.exe cannot carry refuses the launch.
func TestStartBatchViaCmdExe(t *testing.T) {
	t.Run("a batch engine runs through cmd.exe", func(t *testing.T) {
		cmd := &exec.Cmd{Path: npmShim, Args: []string{"claude", "--name", "my session"}}
		require.NoError(t, startBatchViaCmdExe(cmd))
		assert.True(t, strings.EqualFold(filepath.Base(cmd.Path), "cmd.exe"), "path %q", cmd.Path)
		want, err := batchCommandLine(npmShim, []string{"--name", "my session"})
		require.NoError(t, err)
		require.NotNil(t, cmd.SysProcAttr)
		assert.Equal(t, want, cmd.SysProcAttr.CmdLine)
	})
	t.Run("an executable is left as it is", func(t *testing.T) {
		const exe = `C:\Program Files\engine\claude.exe`
		cmd := &exec.Cmd{Path: exe, Args: []string{"claude", "a&b"}}
		require.NoError(t, startBatchViaCmdExe(cmd))
		assert.Equal(t, exe, cmd.Path)
		assert.Nil(t, cmd.SysProcAttr)
	})
	t.Run("an argument cmd.exe cannot carry refuses the launch", func(t *testing.T) {
		cmd := &exec.Cmd{Path: npmShim, Args: []string{"claude", "line one\nline two"}}
		require.ErrorIs(t, startBatchViaCmdExe(cmd), errBatchArgument)
	})
}

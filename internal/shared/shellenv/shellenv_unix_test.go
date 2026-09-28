//go:build !windows

// The login-shell PATH probe exists only where platform.LoginShell is true, which Windows is not.

package shellenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve_LoginShellPathIsCachedAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	fakeBin := filepath.Join(dir, "cached-fake-binary")
	require.NoError(t, os.WriteFile(fakeBin, []byte("#!/bin/sh\n"), 0o755))

	calls := 0
	orig := execCommandContext
	execCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		calls++
		return fakeFencedShellCmd(ctx, "", dir)
	}
	resetCacheForTest()
	t.Cleanup(func() {
		execCommandContext = orig
		resetCacheForTest()
	})

	_, err := Resolve("cached-fake-binary")
	require.NoError(t, err)
	_, err = Resolve("cached-fake-binary")
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "the login shell probe must run at most once per process")
}

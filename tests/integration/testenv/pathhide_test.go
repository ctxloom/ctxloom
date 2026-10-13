//go:build !windows

package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeExec puts an executable called name in dir.
func writeExec(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755))
}

// lookIn is exec.LookPath against an explicit PATH.
func lookIn(t *testing.T, path, name string) (string, error) {
	t.Helper()
	t.Setenv("PATH", path)
	return exec.LookPath(name)
}

func TestPathHiding_HidesEachNamedExecutableAndKeepsItsNeighbours(t *testing.T) {
	engines, tools, scratch := t.TempDir(), t.TempDir(), t.TempDir()
	writeExec(t, engines, "opencode")
	writeExec(t, engines, "helper")
	writeExec(t, tools, "codex")
	writeExec(t, tools, "git")
	path := strings.Join([]string{engines, tools}, string(os.PathListSeparator))

	got, err := PathHiding(path, scratch, "opencode", "codex")
	require.NoError(t, err)

	for _, hidden := range []string{"opencode", "codex"} {
		_, err := lookIn(t, got, hidden)
		assert.Error(t, err, "%s must not resolve through %q", hidden, got)
	}
	for _, kept := range []string{"helper", "git"} {
		p, err := lookIn(t, got, kept)
		require.NoError(t, err, "%s must still resolve through %q", kept, got)
		assert.True(t, strings.HasPrefix(p, scratch), "%s resolves through a mirror under the scratch dir, got %s", kept, p)
	}
}

func TestPathHiding_LeavesADirectoryWithoutANamedExecutableAsItIs(t *testing.T) {
	tools, scratch := t.TempDir(), t.TempDir()
	writeExec(t, tools, "git")

	got, err := PathHiding(tools, scratch, "opencode")
	require.NoError(t, err)
	assert.Equal(t, tools, got)
	entries, err := os.ReadDir(scratch)
	require.NoError(t, err)
	assert.Empty(t, entries, "no mirror is built when nothing is hidden")
}

func TestPathHiding_DropsEmptyEntries(t *testing.T) {
	tools := t.TempDir()
	sep := string(os.PathListSeparator)

	got, err := PathHiding(sep+tools+sep, t.TempDir(), "opencode")
	require.NoError(t, err)
	assert.Equal(t, tools, got, "an empty PATH entry means the cwd, which a hermetic PATH must not search")
}

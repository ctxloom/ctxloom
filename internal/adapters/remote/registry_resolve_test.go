package remote

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newResolveTestRegistry builds a registry with two remotes whose short names
// differ from their URL-derived local names, mirroring a real install.
func newResolveTestRegistry(t *testing.T) *Registry {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "remotes.yaml")
	require.NoError(t, os.WriteFile(path, []byte(
		"remotes:\n"+
			"  personal:\n"+
			"    url: https://github.com/benjaminabbitt/ctxloom-personal\n"+
			"  ctxloom-default:\n"+
			"    url: https://github.com/ctxloom/ctxloom-default\n"), 0o644))
	reg, err := NewRegistry(path)
	require.NoError(t, err)
	return reg
}

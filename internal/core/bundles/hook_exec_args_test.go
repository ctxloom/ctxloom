package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestParseBundle_ReadsExecFormHookArgs: a bundle declares an exec-form hook
// with `args`.
func TestParseBundle_ReadsExecFormHookArgs(t *testing.T) {
	b, err := ParseBundle([]byte("version: \"1.0.0\"\nhooks:\n  session_start:\n    - command: ctxloom\n      args: [hook, session-bind]\n"))
	require.NoError(t, err)
	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Equal(t, []string{"hook", "session-bind"}, b.Hooks.SessionStart[0].Args)
}

// TestLoader_ATreeBundleCarriesExecHookArgs: a directory-form bundle's
// exec-form hook reaches the loaded bundle with its argument list — the tree
// reader drops nothing the preimage binds.
func TestLoader_ATreeBundleCarriesExecHookArgs(t *testing.T) {
	tmpDir := t.TempDir()
	writeTree(t, afero.NewOsFs(), seedBundleRoot(t, tmpDir, paths.LayoutV2), "kit",
		"version: \"1.0\"\nhooks:\n  session_start:\n    - command: ctxloom\n      args: [hook, session-bind]\n      type: command\n")
	b, err := NewLoader(NewProjectReader(nil, []string{tmpDir})).Load("kit")
	require.NoError(t, err)
	require.Len(t, b.Hooks.SessionStart, 1)
	assert.Equal(t, []string{"hook", "session-bind"}, b.Hooks.SessionStart[0].Args)
}

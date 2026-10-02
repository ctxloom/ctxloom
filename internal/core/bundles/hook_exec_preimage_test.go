package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// TestBundleHook_ShellPreimageIsByteIdentical pins the invariance every
// existing grant and countersignature rests on: a shell-form hook's preimage
// is exactly the bytes it always was. Exec form must not reach it.
func TestBundleHook_ShellPreimageIsByteIdentical(t *testing.T) {
	h := BundleHook{Command: "ctxloom hook session-bind", Type: "command", PreToolFallback: true}
	got, err := h.ContentPayload()
	require.NoError(t, err)
	assert.Equal(t,
		`{"preimage":"ctxloom-exec/2","matcher":"","type":"command","command":"ctxloom hook session-bind","prompt":"","pre_tool_fallback":true}`,
		string(got))
}

// TestBundleHook_ExecArgsAreBoundByTheirLine: an exec-form hook's preimage
// binds its argument list through the command line it runs — the same rule as
// a hook ctxloom builds in code (wire.Hook.Line) — so a changed argument never
// rides an earlier grant, and the field set (and so the contract version) is
// unchanged.
func TestBundleHook_ExecArgsAreBoundByTheirLine(t *testing.T) {
	exec := BundleHook{Command: "ctxloom", Args: []string{"hook", "session-bind"}, Type: "command"}
	other := BundleHook{Command: "ctxloom", Args: []string{"hook", "stamp-plan"}, Type: "command"}
	shell := BundleHook{Command: `'ctxloom' 'hook' 'session-bind'`, Type: "command"}
	assert.NotEqual(t, exec.ComputeContentHash(), other.ComputeContentHash(), "the arguments are part of what runs")
	assert.Equal(t, shell.ComputeContentHash(), exec.ComputeContentHash(), "one argv, one preimage")
}

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

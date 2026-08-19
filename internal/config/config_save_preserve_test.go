package config

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestConfig_Save_PreservesCommentsAndKeyOrder pins U049-F16: the persist path
// round-tripped config.yaml through map[string]interface{}, so every write
// destroyed ALL comments and reordered every key (yaml.v3 emits map keys
// sorted, carrying no comments) — the exact opposite of the upgrade path, which
// rewrites the yaml.Node tree precisely so comments and order survive. A save
// that changes ONE unrelated field must leave a hand-authored config's comments
// and key order intact.
func TestConfig_Save_PreservesCommentsAndKeyOrder(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	require.NoError(t, fs.MkdirAll(appDir, 0o755))

	// A hand-authored file: leading comment, a deliberately NON-alphabetical key
	// order (version, editor, custom_unknown, mcp), and inline comments.
	seed := "# ctxloom project config — hand edited, do not clobber\n" +
		"version: 5\n" +
		"editor:\n" +
		"  command: vim # my preferred editor\n" +
		"custom_unknown: keepme # an app ctxloom does not model\n" +
		"mcp:\n" +
		"  servers:\n" +
		"    srv:\n" +
		"      command: x\n"
	path := paths.ConfigPath(appDir)
	require.NoError(t, afero.WriteFile(fs, path, []byte(seed), 0o644))

	// A Config that carries the same editor + mcp (so they are not pruned) and
	// makes ONE unrelated change: set default_agent.
	cfg := &Config{
		appPaths: []string{appDir},
		// SourceHome so the layer-scope save filter is skipped: editor.command is
		// Machine-scoped and would legitimately be dropped from a *project* file,
		// which is a separate concern from the comment/order preservation under
		// test here.
		source:       SourceHome,
		editor:       EditorConfig{Command: "vim"},
		defaultAgent: "reviewer",
	}
	cfg.SetFS(fs)
	require.NoError(t, cfg.saveLocked(fs, path))

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	got := string(data)

	// Comments survive.
	assert.Contains(t, got, "# ctxloom project config", "the leading file comment must survive a save")
	assert.Contains(t, got, "# my preferred editor", "an inline comment on a modeled key must survive")
	assert.Contains(t, got, "# an app ctxloom does not model", "an inline comment on an unknown key must survive")

	// Unknown key + its value survive.
	assert.Contains(t, got, "custom_unknown: keepme", "unknown keys must survive a save")

	// The unrelated change landed and the version was stamped forward.
	assert.Contains(t, got, "reviewer", "the unrelated default_agent change must land")

	// Original key ORDER is preserved (version before editor before
	// custom_unknown), NOT re-sorted alphabetically.
	iVersion := strings.Index(got, "version:")
	iEditor := strings.Index(got, "editor:")
	iCustom := strings.Index(got, "custom_unknown:")
	require.True(t, iVersion >= 0 && iEditor >= 0 && iCustom >= 0)
	assert.Less(t, iVersion, iEditor, "version must stay before editor (authored order preserved)")
	assert.Less(t, iEditor, iCustom, "editor must stay before custom_unknown (authored order preserved)")
}

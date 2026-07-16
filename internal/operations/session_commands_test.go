package operations

import (
	"context"
	"os/exec"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// setupSessionCommandsTestFS mirrors setupPromptTestFS (commands_test.go) but
// wires a REAL *config.Config (via cfg.SetFS) instead of an injected Loader,
// because buildSessionCommands (unlike ListCommands/GetCommand) has no
// Loader-injection field of its own — it is the ACP session-open path's
// production entry point, which always resolves its loader from cfg.
func setupSessionCommandsTestFS(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // never touch the developer's real ~/.ctxloom
	// Companion-loadout probing (cfg.SeededBundleLoader ->
	// companionBundleSeed) shells out via LookPath; stub it closed so this
	// test's result depends only on the bundle file below, never on what
	// happens to be installed on the machine running it.
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound })
	t.Cleanup(restore)

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0o755))
	bundleContent := `version: "1.0"
description: Test bundle with commands
commands:
  code-review:
    description: Review code for issues
    content: |
      # Code Review
      Please review the following code for bugs.
  no-description:
    content: |
      # No Description
      This command never set a description.
`
	require.NoError(t, afero.WriteFile(fs, paths.LocalBundlesPath(testBaseDir)+"/dev-tools.yaml", []byte(bundleContent), 0o644))

	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	cfg.SetFS(fs)
	return cfg
}

// TestBuildSessionCommands_Available proves B4 (gap G5): the ACP agent
// role's command surface is ctxloom's REAL commands, descriptions included —
// never a fabricated placeholder, and never empty-but-silently-wrong when a
// bundle command left `description:` unset.
func TestBuildSessionCommands_Available(t *testing.T) {
	cfg := setupSessionCommandsTestFS(t)

	cmds := buildSessionCommands(context.Background(), cfg)
	require.NotNil(t, cmds)
	require.Len(t, cmds.Available, 2)

	byName := make(map[string]CommandInfo, len(cmds.Available))
	for _, c := range cmds.Available {
		byName[c.Name] = c
	}
	require.Contains(t, byName, "code-review")
	assert.Equal(t, "Review code for issues", byName["code-review"].Description)
	require.Contains(t, byName, "no-description")
	assert.Empty(t, byName["no-description"].Description, "an unset description advertises empty, never a fabricated one")
}

// TestBuildSessionCommands_ResolveExpandsMatchedName proves the SAME
// resolution path `ctxloom run --command <name>` uses (GetCommand) backs a
// recognized "/<name>" prompt invocation, args appended verbatim.
func TestBuildSessionCommands_ResolveExpandsMatchedName(t *testing.T) {
	cfg := setupSessionCommandsTestFS(t)
	cmds := buildSessionCommands(context.Background(), cfg)
	require.NotNil(t, cmds)
	require.NotNil(t, cmds.Resolve)

	text, ok, err := cmds.Resolve(context.Background(), "code-review", "focus on the auth module")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Contains(t, text, "Please review the following code for bugs.")
	assert.Contains(t, text, "focus on the auth module")
}

// TestBuildSessionCommands_ResolveUnknownNamePassesThrough proves an
// unmatched name is reported as NOT resolved (ok=false, no error) — the
// caller (acpagent's expandCommand) must leave text like this untouched,
// since most "/word ..." prompts are ordinary user text, not a command
// invocation.
func TestBuildSessionCommands_ResolveUnknownNamePassesThrough(t *testing.T) {
	cfg := setupSessionCommandsTestFS(t)
	cmds := buildSessionCommands(context.Background(), cfg)
	require.NotNil(t, cmds)

	text, ok, err := cmds.Resolve(context.Background(), "etc", "passwd contains secrets")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, text)
}

// TestBuildSessionCommands_NoCommandsConfigured: an empty command surface
// degrades to nil (no available_commands_update at all), matching
// buildSessionModes/buildSessionLLMs's own fault-tolerant "advertise
// nothing" behavior rather than an empty-but-present frame.
func TestBuildSessionCommands_NoCommandsConfigured(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound })
	t.Cleanup(restore)

	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0o755))
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	cfg.SetFS(fs)

	assert.Nil(t, buildSessionCommands(context.Background(), cfg))
}

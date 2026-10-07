// Companion-command export tests verify LoadCommandExports gives a companion
// binary's loadout commands the SAME unconditional-when-present treatment S8
// gave fragments/hooks/MCP: a companion on PATH (ltk's task-runner command)
// exports as a slash command with no profile wiring required, gated through
// the identical trust decision every other companion surface goes through —
// never the builtin nil-gate exemption. The companions package's
// companion_loadout_test.go holds the sibling hooks/MCP/fragments proofs.
package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// ltkLoadoutWithTaskRunnerCommand is a minimal stand-in for
// cmd/ltk/loadout.yaml's real commands/task-runner entry — just the command,
// so these tests aren't coupled to the production bundle's fragments/hooks
// content.
const ltkLoadoutWithTaskRunnerCommand = `
version: "1.0.0"
commands:
  task-runner:
    description: "Detect and configure the project's task runner"
    content: "ltk task-runner command body"
`

// fakeLtkOnPath points the companion PATH-resolution seam at a fake ltk binary
// and the loadout-probe seam at an envelope wrapping bundleYAML, returning a
// restore function that undoes both.
func fakeLtkOnPath(t *testing.T, bundleYAML string) func() {
	t.Helper()
	envelope := testsupport.RunLoadout(bundleYAML)
	restoreLook := companions.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/fake/ltk", nil
		}
		return "", os.ErrNotExist
	})
	restoreProbe := companions.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return envelope, nil
	})
	return func() {
		restoreLook()
		restoreProbe()
	}
}

// companionCfg builds a bare Config with a real (temp-dir) AppPaths entry —
// companionBundleSeed's probe guard requires a project directory to fire at
// all — and HOME isolated so the trust root read doesn't touch the real
// developer machine.
func companionCfg(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}, Companions: fakedCompanionNames})
}

// TestLoadCommandExports_IncludesCompanionCommandUnconditionally proves ltk's
// task-runner command exports as a slash command purely because ltk is on
// PATH — no profile references it, no bundle: pull, no curation — matching
// how S8 made companion fragments/hooks/MCP unconditional-when-present. It
// also pins the exact slash-command name the ltk loadout's command gets.
func TestLoadCommandExports_IncludesCompanionCommandUnconditionally(t *testing.T) {
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerCommand)()
	cfg := companionCfg(t)

	prompts := commandsOf(t, withCompanions(t, cfg), nil)
	items := bundlePromptItems(prompts)
	require.Contains(t, items, "task-runner", "ltk's companion command must export with no profile wiring")

	ex := claudeExportsOf(t, withCompanions(t, cfg))
	var found bool
	for _, e := range ex {
		if e.Name == "ltk/task-runner" {
			found = true
			assert.True(t, e.Enabled, "a companion command with no llm: block defaults to enabled")
		}
	}
	require.True(t, found, "expected the ltk/task-runner command export")

	// Materialize to prove the actual slash-command filename: "/" becomes "-",
	// so ltk's task-runner command becomes /ltk-task-runner.
	fs := afero.NewMemMapFs()
	require.NoError(t, claude.WriteCommandFiles("/project", ex, agent.WithCommandFS(fs)))
	exists, err := afero.Exists(fs, "/project/.claude/commands/ltk-task-runner.md")
	require.NoError(t, err)
	assert.True(t, exists, "ltk's task-runner command must materialize as /ltk-task-runner")
}

// TestLoadCommandExports_CuratedProfileStillGetsCompanionCommand proves
// companion commands are ADDED to the export set, not a replacement for
// profile command curation: a profile that curates its own bundle command
// still gets exactly that command, AND the companion's unconditional command,
// together.
func TestLoadCommandExports_CuratedProfileStillGetsCompanionCommand(t *testing.T) {
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerCommand)()
	cfg := companionCfg(t)
	appDir := cfg.GetAppPaths()[0]
	require.NoError(t, os.MkdirAll(bundletree.ProjectProfilesDir(t, appDir), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundletree.ProjectProfilesDir(t, appDir), "p.yaml"),
		[]byte("commands:\n  - dev-tools#commands/review\n"), 0o644))
	cfg = config.NewFixture(config.Fixture{
		AppPaths:     cfg.GetAppPaths(),
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
		Companions:   fakedCompanionNames,
	})

	prompts := commandsOf(t, withSeedAndCompanions(t, cfg, devToolsSeed()), nil)
	items := bundlePromptItems(prompts)
	assert.Contains(t, items, "review", "the profile's curated command must still export")
	assert.Contains(t, items, "task-runner", "the companion's command must ALSO export under curation, not be replaced by it")
}

// TestLoadCommandExports_NoCompanionOnPath_NoCommandExported is the RED-state
// control: with ltk absent from PATH entirely, its command contributes
// nothing (companionBundleSeed's own PATH-probe skip, no different from the
// hooks/MCP/fragments resolvers).
func TestLoadCommandExports_NoCompanionOnPath_NoCommandExported(t *testing.T) {
	cfg := companionCfg(t)
	restoreLook := companions.SetLookPathForTesting(func(string) (string, error) { return "", os.ErrNotExist })
	defer restoreLook()

	prompts := commandsOf(t, withCompanions(t, cfg), nil)
	assert.NotContains(t, bundlePromptItems(prompts), "task-runner",
		"absent from PATH, ltk contributes no command export")
}

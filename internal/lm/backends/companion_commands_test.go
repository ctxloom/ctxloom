// Companion-command export tests verify LoadCommandExports gives a companion
// binary's loadout commands the SAME unconditional-when-present treatment S8
// gave fragments/hooks/MCP: a companion on PATH (ltk's task-runner command)
// exports as a slash command with no profile wiring required, gated through
// the identical trust decision every other companion surface goes through —
// never the builtin nil-gate exemption. See internal/config/companion_loadout_test.go
// for the sibling hooks/MCP/fragments proofs this mirrors.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/signing"
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
	envelope, err := signing.EncodeLoadoutEnvelope([]byte(bundleYAML), nil, "")
	require.NoError(t, err)
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/fake/ltk", nil
		}
		return "", os.ErrNotExist
	})
	restoreProbe := config.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return envelope, nil
	})
	return func() {
		restoreLook()
		restoreProbe()
	}
}

// companionCfg builds a bare Config with a real (temp-dir) AppPaths entry —
// companionBundleSeed's probe guard requires a project directory to fire at
// all (see TestSeededBundleLoader_NoAppPaths_SkipsCompanionProbing) — and HOME
// isolated so the trust root read doesn't touch the real developer machine.
func companionCfg(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// TestLoadCommandExports_IncludesCompanionCommandUnconditionally proves ltk's
// task-runner command exports as a slash command purely because ltk is on
// PATH — no profile references it, no bundle: pull, no curation — matching
// how S8 made companion fragments/hooks/MCP unconditional-when-present. It
// also pins the exact slash-command name the ltk loadout's command gets.
func TestLoadCommandExports_IncludesCompanionCommandUnconditionally(t *testing.T) {
	// This test's subject is command EXPORT, not companion admission: grant
	// exec consent for the fake ltk so the trust-on-first-use gate does not
	// withhold the loadout before there is anything to export.
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerCommand)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })

	prompts := LoadCommandExports(cfg, nil)
	items := bundlePromptItems(prompts)
	require.Contains(t, items, "task-runner", "ltk's companion command must export with no profile wiring")

	ex := CommandExportsFor("claude-code", prompts)
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

// TestLoadCommandExports_WithheldCompanionCommand_DenyingGateNotBuiltinExemption
// proves the red line S8 held for fragments/hooks/MCP: a denying trust gate
// withholds the companion command, so it is NOT the builtin nil-gate exemption
// (a true builtin would still export under a denying gate below rejection).
func TestLoadCommandExports_WithheldCompanionCommand_DenyingGateNotBuiltinExemption(t *testing.T) {
	// This test's subject is the DENYING CONTENT GATE, so exec consent must be
	// granted: without it the companion is never run at all, and the assertion
	// below would pass because nothing was ever produced rather than because
	// the gate withheld it — the same green-for-the-wrong-reason shape as
	// trust_surface.feature:269.
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerCommand)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return false })

	prompts := LoadCommandExports(cfg, nil)
	assert.NotContains(t, bundlePromptItems(prompts), "task-runner",
		"an unsigned/withheld companion loadout must not export its commands as slash commands")
}

// TestLoadCommandExports_CuratedProfileStillGetsCompanionCommand proves
// companion commands are ADDED to the export set, not a replacement for
// profile command curation: a profile that curates its own bundle command
// still gets exactly that command, AND the companion's unconditional command,
// together.
func TestLoadCommandExports_CuratedProfileStillGetsCompanionCommand(t *testing.T) {
	// This test's subject is command EXPORT, not companion admission: grant
	// exec consent for the fake ltk so the trust-on-first-use gate does not
	// withhold the loadout before there is anything to export.
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerCommand)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
	cfg = config.NewFixture(config.Fixture{
		AppPaths:     cfg.GetAppPaths(),
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"p"}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"p": {Commands: []string{"dev-tools#commands/review"}},
		}},
	})
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })

	prompts := LoadCommandExports(cfg, nil, bundles.WithSeededBundles(devToolsSeed()))
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
	restoreLook := config.SetLookPathForTesting(func(string) (string, error) { return "", os.ErrNotExist })
	defer restoreLook()
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })

	prompts := LoadCommandExports(cfg, nil)
	assert.NotContains(t, bundlePromptItems(prompts), "task-runner",
		"absent from PATH, ltk contributes no command export")
}

// Companion-skill export tests verify LoadSkillExports gives a companion
// binary's loadout skills the SAME unconditional-when-present treatment S8
// gave fragments/hooks/MCP: a companion on PATH (ltk's task-runner skill)
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

// ltkLoadoutWithTaskRunnerSkill is a minimal stand-in for cmd/ltk/loadout.yaml's
// real skills/task-runner entry — just the skill, so these tests aren't coupled
// to the production bundle's fragments/hooks content.
const ltkLoadoutWithTaskRunnerSkill = `
version: "1.0.0"
skills:
  task-runner:
    description: "Detect and configure the project's task runner"
    content: "ltk task-runner skill body"
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
	return &config.Config{AppPaths: []string{appDir}}
}

// TestLoadSkillExports_IncludesCompanionSkillUnconditionally proves ltk's
// task-runner skill exports as a slash command purely because ltk is on
// PATH — no profile references it, no bundle: pull, no curation — matching
// how S8 made companion fragments/hooks/MCP unconditional-when-present. It
// also pins the exact slash-command name the ltk loadout's skill gets.
func TestLoadSkillExports_IncludesCompanionSkillUnconditionally(t *testing.T) {
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerSkill)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })

	prompts := LoadSkillExports(cfg, nil)
	items := bundlePromptItems(prompts)
	require.Contains(t, items, "task-runner", "ltk's companion skill must export with no profile wiring")

	ex := CommandExportsFor("claude-code", prompts)
	var found bool
	for _, e := range ex {
		if e.Name == "ltk/task-runner" {
			found = true
			assert.True(t, e.Enabled, "a companion skill with no llm: block defaults to enabled")
		}
	}
	require.True(t, found, "expected the ltk/task-runner command export")

	// Materialize to prove the actual slash-command filename: "/" becomes "-",
	// so ltk's task-runner skill becomes /ltk-task-runner.
	fs := afero.NewMemMapFs()
	require.NoError(t, claude.WriteCommandFiles("/project", ex, agent.WithCommandFS(fs)))
	exists, err := afero.Exists(fs, "/project/.claude/commands/ltk-task-runner.md")
	require.NoError(t, err)
	assert.True(t, exists, "ltk's task-runner skill must materialize as /ltk-task-runner")
}

// TestLoadSkillExports_WithheldCompanionSkill_DenyingGateNotBuiltinExemption
// proves the red line S8 held for fragments/hooks/MCP: a denying trust gate
// withholds the companion skill, so it is NOT the builtin nil-gate exemption
// (a true builtin would still export under a denying gate below rejection).
func TestLoadSkillExports_WithheldCompanionSkill_DenyingGateNotBuiltinExemption(t *testing.T) {
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerSkill)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return false })

	prompts := LoadSkillExports(cfg, nil)
	assert.NotContains(t, bundlePromptItems(prompts), "task-runner",
		"an unsigned/withheld companion loadout must not export its skills as slash commands")
}

// TestLoadSkillExports_CuratedProfileStillGetsCompanionSkill proves companion
// skills are ADDED to the export set, not a replacement for profile prompt
// curation: a profile that curates its own bundle skill still gets exactly
// that skill, AND the companion's unconditional skill, together.
func TestLoadSkillExports_CuratedProfileStillGetsCompanionSkill(t *testing.T) {
	defer fakeLtkOnPath(t, ltkLoadoutWithTaskRunnerSkill)()
	cfg := companionCfg(t)
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })
	cfg.DefaultAgent = "default"
	cfg.Agents = map[string]agents.Agent{"default": {Profiles: []string{"p"}}}
	cfg.Profiles = config.ProfilesConfig{Definitions: map[string]config.Profile{
		"p": {Prompts: []string{"dev-tools#skills/review"}},
	}}

	prompts := LoadSkillExports(cfg, nil, bundles.WithSeededBundles(devToolsSeed()))
	items := bundlePromptItems(prompts)
	assert.Contains(t, items, "review", "the profile's curated skill must still export")
	assert.Contains(t, items, "task-runner", "the companion's skill must ALSO export under curation, not be replaced by it")
}

// TestLoadSkillExports_NoCompanionOnPath_NoSkillExported is the RED-state
// control: with ltk absent from PATH entirely, its skill contributes nothing
// (companionBundleSeed's own PATH-probe skip, no different from the
// hooks/MCP/fragments resolvers).
func TestLoadSkillExports_NoCompanionOnPath_NoSkillExported(t *testing.T) {
	cfg := companionCfg(t)
	restoreLook := config.SetLookPathForTesting(func(string) (string, error) { return "", os.ErrNotExist })
	defer restoreLook()
	cfg.SetExecutableTrustGate(func(string, []byte, string, string) bool { return true })

	prompts := LoadSkillExports(cfg, nil)
	assert.NotContains(t, bundlePromptItems(prompts), "task-runner",
		"absent from PATH, ltk contributes no skill export")
}

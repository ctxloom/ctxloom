// Package fromv5_test proves the migration OFF config version 5 end to end, through
// the exported load path a user actually takes.
//
// It lives in this directory rather than in config's own test files so that
// retiring support for v5 deletes the migration and its proof together — the
// directory is the unit of support.
package fromv5_test

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// runConfigUpgrades writes in as this project's config.yaml and drives the REAL
// public load path over it, returning the upgraded document and the names of
// the steps that fired.
//
// It goes through config.Load rather than reaching for the pipeline directly
// for two reasons. This package is imported BY config, so it cannot import it
// back except from an external test package like this one — and driving the
// exported entry point is what makes this a proof that the migration a USER
// gets actually works, rather than a proof that a function this test hand-wired
// works. The upgraded bytes are read back off the pending upgrade, which is the
// same value the interactive rewrite prompt persists, so byte-level assertions
// (comments, indentation) stay available.
func runConfigUpgrades(t *testing.T, in string) (root map[string]any, applied []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	pending := cfg.GetPendingUpgrade()
	if pending == nil {
		return nil, nil
	}
	require.NoError(t, yaml.Unmarshal(pending.Data, &root))
	return root, pending.Applied
}

// upgradedBytes is runConfigUpgrades for an assertion about the FILE rather
// than the parsed shape: comment and indent preservation. Returns the input
// verbatim when no step fired, which is what "unchanged" means on disk.
func upgradedBytes(t *testing.T, in string) (out []byte, applied []string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	if pending := cfg.GetPendingUpgrade(); pending != nil {
		return pending.Data, pending.Applied
	}
	return []byte(in), nil
}

// lossyWarnings returns the dropped-setting diagnostics the load recorded, as
// plain strings.
//
// It reads them off the LOADED CONFIG rather than draining a package-global
// buffer, which is what the pre-move version of these tests had to do. That is
// not merely a mechanical change: a global buffer is shared with every other
// test in the binary, which is why those tests opened by draining it to discard
// "any earlier test's residue". Reading the warnings attached to the config
// under test removes the residue problem instead of working around it, and
// matches what the strict startup gate actually inspects.
func lossyWarnings(t *testing.T, in string) []string {
	t.Helper()
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir), []byte(in), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)
	var out []string
	for _, w := range cfg.GetWarnings() {
		if w.Kind == config.WarnKindMigrationLossy {
			out = append(out, w.Text)
		}
	}
	return out
}

// llmMap is a typed accessor for root["llm"] in a parsed upgraded config.
func llmMap(t *testing.T, root map[string]any) map[string]any {
	t.Helper()
	llm, ok := root["llm"].(map[string]any)
	require.True(t, ok, "llm should be a mapping")
	return llm
}

// TestConfigUpgrades_V5toV6_DefaultAgent pins the profiles.defaults → default
// agent migration: a v5 config's default profile list becomes the synthesized
// "default" agent's profiles, default_agent points at it, and the agent carries
// the primary LLM label as its engine + host runtime.
func TestConfigUpgrades_V5toV6_DefaultAgent(t *testing.T) {
	in := "version: 5\n" +
		"llm:\n  configs:\n    big: { type: claude-code }\n  defaults:\n    primary: big\n" +
		"profiles:\n  defaults:\n    - dev\n    - go\n  definitions:\n    dev:\n      bundles: [a/b]\n"
	root, applied := runConfigUpgrades(t, in)
	require.Contains(t, applied, "profiles.defaults → default agent (v5→v6)")

	assert.Equal(t, 6, root["version"])
	assert.Equal(t, "default", root["default_agent"])

	agent := root["agents"].(map[string]any)["default"].(map[string]any)
	assert.Equal(t, []any{"dev", "go"}, agent["profiles"])
	assert.Equal(t, "big", agent["llm"], "llm ← llm.defaults.primary")
	assert.NotContains(t, agent, "runtime", "runtime is deliberately left unset -- see the identical assertion above")

	// profiles.defaults is gone; definitions survive.
	prof := root["profiles"].(map[string]any)
	assert.NotContains(t, prof, "defaults")
	assert.Contains(t, prof["definitions"].(map[string]any), "dev")
}

// A v5 config without profiles.defaults only gains the version stamp.

// A v5 config without profiles.defaults only gains the version stamp.
func TestConfigUpgrades_V5toV6_NoDefaultsOnlyStamps(t *testing.T) {
	in := "version: 5\nllm:\n  configs:\n    big: { type: claude-code }\n  defaults:\n    primary: big\n"
	out, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "version: 6")
	assert.NotContains(t, string(out), "default_agent")
	assert.NotContains(t, string(out), "agents:")
}

func TestConfigUpgrades_V5toV6_PreservesComments(t *testing.T) {
	in := "# header\nversion: 5\nllm:\n  defaults:\n    primary: big\nprofiles:\n  defaults:\n    # keep me\n    - dev\n"
	out, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	assert.Contains(t, string(out), "# header", "top-level comment survives")
	assert.Contains(t, string(out), "# keep me", "the moved defaults-seq item comment survives")
	assert.Contains(t, string(out), "version: 6")
}

func TestConfigUpgrades_V5toV6_Idempotent(t *testing.T) {
	in := "version: 5\nllm:\n  defaults:\n    primary: big\nprofiles:\n  defaults:\n    - dev\n"
	once, applied := upgradedBytes(t, in)
	require.NotEmpty(t, applied)
	twice, again := upgradedBytes(t, string(once))
	assert.Empty(t, again, "already-v6 config must pass through unchanged")
	assert.Equal(t, string(once), string(twice))
}

// A hand-authored agents.default / default_agent wins; the retired
// profiles.defaults is still dropped.

// A hand-authored agents.default / default_agent wins; the retired
// profiles.defaults is still dropped.
func TestConfigUpgrades_V5toV6_DoesNotClobberExistingDefaultAgent(t *testing.T) {
	in := "version: 5\ndefault_agent: mine\nagents:\n  default:\n    profiles: [keep]\nprofiles:\n  defaults:\n    - dev\n"
	root, _ := runConfigUpgrades(t, in)
	assert.Equal(t, "mine", root["default_agent"], "existing default_agent is not clobbered")
	agent := root["agents"].(map[string]any)["default"].(map[string]any)
	assert.Equal(t, []any{"keep"}, agent["profiles"], "existing agents.default is not clobbered")
	// The retired profiles.defaults is dropped; the profiles map (empty here) is pruned.
	assert.NotContains(t, root, "profiles")
}

// The collision branch — a hand-authored agents.default already exists, so
// profiles.defaults cannot be synthesized into it — DELETES the user's
// default profile list and says nothing. That is an irreversible on-disk
// loss (the migration rewrites the file), and the next run silently launches
// with a different profile set than the one the user configured.
//
// migrateLLMv3 already does the right thing for its own lossy branch:
// recordMigrationWarning, surfaced as WarnKindMigrationLossy, fatal in strict
// mode. This branch is the same class of loss and must be reported the same
// way.
func TestConfigUpgrades_V5toV6_CollidingDefaults_IsReportedLossy(t *testing.T) {

	in := "version: 5\nagents:\n  default:\n    profiles: [keep]\nprofiles:\n  defaults:\n    - dev\n    - go\n"
	root, _ := runConfigUpgrades(t, in)

	agent := root["agents"].(map[string]any)["default"].(map[string]any)
	require.Equal(t, []any{"keep"}, agent["profiles"], "precondition: the collision branch ran")

	warnings := lossyWarnings(t, in)
	require.NotEmpty(t, warnings, "dropping the user's profiles.defaults must be recorded as a lossy migration")
	joined := strings.Join(warnings, "\n")
	assert.Contains(t, joined, "profiles.defaults")
	assert.Contains(t, joined, "dev")
	assert.Contains(t, joined, "go")
}

// The SYNTHESIZING branch loses nothing — the list is moved, not dropped — so
// it must stay quiet. A warning on the ordinary path is noise every upgrading
// user would see.

// The SYNTHESIZING branch loses nothing — the list is moved, not dropped — so
// it must stay quiet. A warning on the ordinary path is noise every upgrading
// user would see.
func TestConfigUpgrades_V5toV6_SynthesizedDefaults_IsNotLossy(t *testing.T) {

	in := "version: 5\nllm:\n  defaults:\n    primary: big\nprofiles:\n  defaults:\n    - dev\n"
	root, _ := runConfigUpgrades(t, in)
	agent := root["agents"].(map[string]any)["default"].(map[string]any)
	require.Equal(t, []any{"dev"}, agent["profiles"], "precondition: the synthesizing branch ran")

	assert.Empty(t, lossyWarnings(t, in), "a migration that moved the list lost nothing")
}

// A config whose `version:` key is present but not an integer is not a
// pre-versioning document — it is a document whose version cannot be read, and
// the two must not be treated the same. Reading it as generation 0 re-ran every
// migration from the start over a file that is probably corrupt, and stamped
// the current version on the way out: the loud "cannot unmarshal `banana` into
// int" the caller would have got is replaced by a clean parse of a rewritten
// file the user is then prompted to persist.

// Strictness must be applied AFTER migration: an older config whose keys the
// migrator upgrades forward must load CLEAN. Otherwise every user on a
// migratable config eats a fatal finding for a key ctxloom itself would fix.
func TestLoad_OldVersionWithMigratableKey_MigratesWithoutWarning(t *testing.T) {
	fs := afero.NewMemMapFs()
	appDir := "/project/" + paths.AppDirName
	require.NoError(t, fs.MkdirAll(appDir, 0o755))
	require.NoError(t, afero.WriteFile(fs, paths.ConfigPath(appDir),
		[]byte("version: 5\nprofiles:\n  defaults:\n    - dev\n  definitions:\n    dev:\n      description: dev\n"), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	assert.Empty(t, cfg.GetWarnings(), "a migratable old-version config must produce NO warnings: %+v", cfg.GetWarnings())
	require.NotNil(t, cfg.GetPendingUpgrade(), "the load must have upgraded the document in memory")
	assert.Equal(t, "default", cfg.GetDefaultAgent(), "the v5→v6 migration rehomes profiles.defaults onto the default agent")
	assert.Equal(t, []string{"dev"}, cfg.DefaultAgentProfiles())
}

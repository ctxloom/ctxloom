package config_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/configload"
	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writeAppConfig(t *testing.T, appDir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
}

// TestConfig_ParsesAgentsKey proves the `agents:` config key is parsed
// into config.Config.Agents — the config-key source of the agent entity.
func TestConfig_ParsesAgentsKey(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    llm: claude-code
    profiles: [go-developer, go-style]
  finder:
    profiles: [finder]
`)
	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, cfg.GetConfiguredAgents(), 2)
	assert.Equal(t, "claude-code", cfg.GetConfiguredAgents()["dev"].LLM)
	assert.Equal(t, []string{"go-developer", "go-style"}, cfg.GetConfiguredAgents()["dev"].Profiles)
	assert.Empty(t, cfg.GetConfiguredAgents()["finder"].LLM, "engine is optional")
}

// TestConfig_LegacySubagentsKey_WarnsNeverErrors pins the v0.7.0 rename
// contract: the retired `subagents:` key is NOT parsed (no compat shim —
// re-init is the upgrade path) but a config still carrying it must load with
// a schema warning naming the stray key, never a hard error (CLAUDE.md fault
// tolerance: the old bindings go inert, startup proceeds). Unknown-key
// preservation on save stays generic — an old block may survive a rewrite
// verbatim; that is deliberate, not a migration.
func TestConfig_LegacySubagentsKey_WarnsNeverErrors(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
subagents:
  dev:
    llm: claude-code
    profiles: [go-developer]
`)
	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err, "a legacy key must never block startup")
	assert.Empty(t, cfg.GetConfiguredAgents(), "the retired key is inert, not migrated")
	assert.Empty(t, cfg.LoadAgents())
	warned := false
	for _, w := range cfg.ToFixture().Warnings {
		if strings.Contains(w.Text, "subagents") {
			warned = true
			break
		}
	}
	assert.True(t, warned, "schema validation should warn about the stray subagents key so the rename is diagnosable; warnings: %v", cfg.ToFixture().Warnings)
}

// TestConfigSchema_AcceptsAgents pins the schema to the parser: a config with
// an `agents:` block must validate (top-level additionalProperties:false would
// otherwise reject it — exactly how `sync` once silently broke).
func TestConfigSchema_AcceptsAgents(t *testing.T) {
	v, err := schema.NewConfigValidator()
	require.NoError(t, err)
	yaml := `agents:
  dev:
    llm: claude-code
    profiles: [go-developer]
  finder:
    profiles: [finder]
`
	assert.NoError(t, v.ValidateBytes([]byte(yaml)))
}

// TestLoadAgents_ReadsTheConfigKey proves the agent view is the `agents:`
// config key alone, sorted by name, with every declared axis intact. Asserting
// the axes and not merely the lookup is the point: a binding that resolves
// while dropping what it declares is the failure worth catching.
func TestLoadAgents_ReadsTheConfigKey(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// home-layer read never reaches this developer's real ~/.ctxloom — only
	// the appDir fixture built below is meant to contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    llm: claude-code
    profiles: [go-developer]
    runtime: container-rootless
    permissions: bypass
    driving: oneshot
    engine_home: session
    surfaces:
      context: system-prompt
  finder:
    profiles: [finder]
`)

	cfg, err := configload.Load(configload.WithAppDir(appDir))
	require.NoError(t, err)

	subs := cfg.LoadAgents()
	require.Len(t, subs, 2)
	assert.Equal(t, "dev", subs[0].Name)
	assert.Equal(t, "finder", subs[1].Name, "sorted by name")

	dev, ok := cfg.Agent("dev")
	require.True(t, ok)
	// Assert the VALUES, not merely that the lookup succeeded: engine_home and
	// surfaces are the two axes a launch degrades to a default on, so a binding
	// that resolves while dropping them is the failure worth catching.
	assert.Equal(t, "claude-code", dev.LLM)
	assert.Equal(t, []string{"go-developer"}, dev.Profiles)
	assert.Equal(t, "container-rootless", dev.Runtime)
	assert.Equal(t, "bypass", dev.Permissions)
	assert.Equal(t, agents.DrivingOneshot, dev.Driving)
	// Binding.HomeMode stays a RAW string on purpose — a hand-edited
	// config.yaml can hold anything, and agents.ParseHomeMode is what turns it
	// into the typed vocabulary. Asserting the typed constant here compares
	// agents.HomeMode against string and fails on the type, not the value.
	assert.Equal(t, "session", dev.HomeMode)
	assert.Equal(t, map[string]string{"context": "system-prompt"}, dev.Surfaces)

	_, ok = cfg.Agent("absent")
	assert.False(t, ok)
}

// TestLoadAgents_RetiredDirectoryIsAFatalFinding pins both halves of what a
// .ctxloom/agents directory gets: its files are NOT read, and they are NOT
// silently ignored either. Exit 0 over a binding the user wrote and ctxloom
// quietly dropped is this codebase's characteristic bug.
func TestLoadAgents_RetiredDirectoryIsAFatalFinding(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	appPath := "/app"
	agentsDir := paths.AgentsPath(appPath)
	require.NoError(t, mem.MkdirAll(agentsDir, 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(agentsDir, "finder.yaml"),
		[]byte("llm: fast\nprofiles: [finder]\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{appPath},
		Agents:   map[string]agents.Agent{"dev": {LLM: "claude-code", Profiles: []string{"go-developer"}}},
	})
	cfg.SetFS(mem)
	cfg.SetReporter(strictness.Sink("ctxloom"))

	mark := strictness.Checkpoint()
	got := cfg.LoadAgents()

	// The config key still resolves — a stranded directory does not take the
	// working half of the config down with it.
	require.Len(t, got, 1, "the directory contributes nothing")
	assert.Equal(t, "dev", got[0].Name)

	findings := strictness.Since(mark)
	require.Len(t, findings, 1, "a directory holding definitions nothing reads must be reported")
	assert.Equal(t, strictness.ClassMigration, findings[0].Class)
	assert.Contains(t, findings[0].Message, "finder.yaml", "the finding names the file the user must move")
	assert.Contains(t, findings[0].Message, agentsDir)
	assert.Contains(t, findings[0].FixIt, paths.ConfigPath(appPath), "the fix-it names where the binding belongs")
}

// TestLoadAgents_AbsentOrEmptyDirectoryIsSilent is the other half: the signpost
// is guarded, not unconditional. Without this the assertion above could pass
// because every project fires the finding.
func TestLoadAgents_AbsentOrEmptyDirectoryIsSilent(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	appPath := "/quiet"
	// An EMPTY directory (or a stray non-YAML file) is not a stranded
	// definition — there is nothing for the user to move.
	require.NoError(t, mem.MkdirAll(paths.AgentsPath(appPath), 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(paths.AgentsPath(appPath), "README.md"),
		[]byte("notes\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{appPath},
		Agents:   map[string]agents.Agent{"dev": {Profiles: []string{"p"}}},
	})
	cfg.SetFS(mem)
	cfg.SetReporter(strictness.Sink("ctxloom"))

	mark := strictness.Checkpoint()
	require.Len(t, cfg.LoadAgents(), 1)
	assert.Empty(t, strictness.Since(mark),
		"an empty retired directory has nothing to report; only definitions do")
}

// TestConfig_SaveRoundTripsAgents proves the config-key agents survive a
// Save (so a programmatic write — e.g. Phase F's agent-assisted setup —
// persists), while directory agents stay in their files.
func TestConfig_SaveRoundTripsAgents(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, "version: 5\n")
	src, err := configload.New(nil, nil, configload.WithAppDir(appDir))
	require.NoError(t, err)
	owner, err := config.Open(context.Background(), src)
	require.NoError(t, err)

	next, err := owner.Update(context.Background(), func(d *config.Draft) error {
		d.Agents = map[string]agents.Agent{
			"dev": {LLM: "claude-code", Profiles: []string{"go-developer"}},
		}
		return nil
	})
	require.NoError(t, err)

	got := next.Config.GetConfiguredAgents()
	require.Len(t, got, 1)
	assert.Equal(t, "claude-code", got["dev"].LLM)
	assert.Equal(t, []string{"go-developer"}, got["dev"].Profiles)
}

// LoadAgents folded the `agents:` config-key entries into its merged
// map with a plain struct copy, so every returned Agent's Profiles and
// Escalation slices still pointed at the shared config.Config's storage. That bypasses
// the copy-on-read policy accessors.go exists to enforce — and the package
// already owns the right helper (cloneAgent), it simply was not on this path.
//
// It matters more than an ordinary aliasing hole: Agent.Escalation is the
// permission ladder a delegated child runs under, and Agent.Profiles decides
// which context that child is given. A caller that filters or reorders either
// in place would be rewriting them for every other holder of the ambient
// config.Config, silently.

// Agent(name) re-runs LoadAgents on every lookup, and one command reaches it
// several times (ResolveAgent, DefaultAgentProfiles, `agent show`), so the
// retired-directory signpost must state its fact once rather than stack N
// identical findings and N identical stderr lines into one abort listing.
func TestLoadAgents_RetiredDirectoryFindingIsRecordedOncePerWindow(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	appPath := "/repeatprobe"
	agentsDir := paths.AgentsPath(appPath)
	require.NoError(t, mem.MkdirAll(agentsDir, 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(agentsDir, "stranded.yaml"),
		[]byte("profiles: [from-disk]\n"), 0o644))

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{appPath},
		Agents:   map[string]agents.Agent{"dev": {Profiles: []string{"from-config"}}},
	})
	cfg.SetFS(mem)
	cfg.SetReporter(strictness.Sink("ctxloom"))

	mark := strictness.Checkpoint()
	for range 3 {
		got, ok := cfg.Agent("dev")
		require.True(t, ok, "the config key must still resolve while a stranded directory sits there")
		require.Equal(t, []string{"from-config"}, got.Profiles)
	}
	_, ok := cfg.Agent("stranded")
	assert.False(t, ok, "a file under the retired directory defines nothing")

	assert.Len(t, strictness.Since(mark), 1,
		"the signpost states a fact about the config once per window; repeating it per lookup is noise")
}

func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	t.Cleanup(func() {
		strictness.Reset()
	})
}

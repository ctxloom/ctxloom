package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func writeAppConfig(t *testing.T, appDir, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
}

// TestConfig_ParsesAgentsKey proves the `agents:` config key is parsed
// into Config.Agents — the config-key source of the agent entity.
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
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, cfg.agents, 2)
	assert.Equal(t, "claude-code", cfg.agents["dev"].LLM)
	assert.Equal(t, []string{"go-developer", "go-style"}, cfg.agents["dev"].Profiles)
	assert.Empty(t, cfg.agents["finder"].LLM, "engine is optional")
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
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err, "a legacy key must never block startup")
	assert.Empty(t, cfg.agents, "the retired key is inert, not migrated")
	assert.Empty(t, cfg.LoadAgents())
	warned := false
	for _, w := range cfg.warnings {
		if strings.Contains(w.Text, "subagents") {
			warned = true
			break
		}
	}
	assert.True(t, warned, "schema validation should warn about the stray subagents key so the rename is diagnosable; warnings: %v", cfg.warnings)
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

// TestLoadAgents_MergesBothSources proves the merged view folds the config
// key AND the .ctxloom/agents/*.yaml directory, each entry carrying its
// Source, sorted by name.
func TestLoadAgents_MergesBothSources(t *testing.T) {
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
    profiles: [go-developer]
`)
	subDir := filepath.Join(appDir, "agents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "finder.yaml"),
		[]byte("llm: fast\nprofiles: [finder]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	subs := cfg.LoadAgents()
	require.Len(t, subs, 2)
	assert.Equal(t, "dev", subs[0].Name)
	assert.Equal(t, agents.SourceConfig, subs[0].Source)
	assert.Equal(t, "finder", subs[1].Name)
	assert.Equal(t, filepath.Join(subDir, "finder.yaml"), subs[1].Source)

	got, ok := cfg.Agent("finder")
	require.True(t, ok)
	assert.Equal(t, "fast", got.LLM)
	_, ok = cfg.Agent("absent")
	assert.False(t, ok)
}

// TestLoadAgents_ConfigWinsOnCollision proves a name defined in BOTH sources
// resolves to the config-key definition (the directory one is shadowed).
func TestLoadAgents_ConfigWinsOnCollision(t *testing.T) {
	// The config.yaml read is real-OS-fs (no WithFS): isolate HOME so the
	// new home-layer read (D2/D3 layering) never reaches this developer's
	// real ~/.ctxloom — only the appDir fixture built below is meant to
	// contribute anything.
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	writeAppConfig(t, appDir, `version: 5
agents:
  dev:
    llm: from-config
    profiles: [config-profile]
`)
	subDir := filepath.Join(appDir, "agents")
	require.NoError(t, os.MkdirAll(subDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "dev.yaml"),
		[]byte("llm: from-dir\nprofiles: [dir-profile]\n"), 0644))

	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	dev, ok := cfg.Agent("dev")
	require.True(t, ok)
	assert.Equal(t, "from-config", dev.LLM, "config-key entry wins on collision")
	assert.Equal(t, agents.SourceConfig, dev.Source)
	assert.Len(t, cfg.LoadAgents(), 1, "the shadowed directory entry is not a second agent")
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
	cfg, err := Load(WithAppDir(appDir))
	require.NoError(t, err)

	cfg.agents = map[string]agents.Agent{
		"dev": {LLM: "claude-code", Profiles: []string{"go-developer"}},
	}
	configPath, err := cfg.GetConfigFilePath()
	require.NoError(t, err)
	require.NoError(t, cfg.saveLocked(cfg.getFS(), configPath))

	reloaded, err := Load(WithAppDir(appDir))
	require.NoError(t, err)
	require.Len(t, reloaded.agents, 1)
	assert.Equal(t, "claude-code", reloaded.agents["dev"].LLM)
	assert.Equal(t, []string{"go-developer"}, reloaded.agents["dev"].Profiles)
}

// TestAgentDirLoader_NeverNil pins the invariant LoadAgents relies on: the
// directory loader is always usable, so LoadAgents needs no nil guard of its
// own. With no app paths at all, GetAgentDirs yields an empty slice and
// agents.NewLoader still returns a loader whose List() is the empty case.
func TestAgentDirLoader_NeverNil(t *testing.T) {
	cfg := &Config{}
	loader := cfg.agentDirLoader()
	require.NotNil(t, loader, "agentDirLoader must never return nil")

	list, err := loader.List()
	require.NoError(t, err)
	assert.Empty(t, list)
}

// LoadAgents folded the `agents:` config-key entries into its merged
// map with a plain struct copy, so every returned Agent's Profiles and
// Escalation slices still pointed at the shared Config's storage. That bypasses
// the copy-on-read policy accessors.go exists to enforce — and the package
// already owns the right helper (cloneAgent), it simply was not on this path.
//
// It matters more than an ordinary aliasing hole: Agent.Escalation is the
// permission ladder a delegated child runs under, and Agent.Profiles decides
// which context that child is given. A caller that filters or reorders either
// in place would be rewriting them for every other holder of the ambient
// Config, silently.

// TestLoadAgents_NeverAliasesConfigContainers is the class gate — a slice field
// added to agents.Agent tomorrow and not cloned fails here.
func TestLoadAgents_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	list := cfg.LoadAgents()
	require.NotEmpty(t, list, "the probe fixture must define an agent, or this gate proves nothing")

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(list), "Config", "LoadAgents")
}

// TestLoadAgents_MutationDoesNotReachConfig states it as behaviour, on the two
// fields F05 named.
func TestLoadAgents_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	for _, a := range cfg.LoadAgents() {
		require.NotEmpty(t, a.Profiles)
		require.NotEmpty(t, a.Escalation)
		a.Profiles[0] = "MUTATED"
		a.Escalation[0].Kinds[0] = "MUTATED"
		a.Escalation[0].Action = "MUTATED"
	}

	worker := cfg.GetConfiguredAgents()["worker"]
	assert.Equal(t, []string{"p"}, worker.Profiles,
		"LoadAgents must hand back an owned copy of Profiles")
	assert.Equal(t, []string{"TOOL_USE"}, worker.Escalation[0].Kinds,
		"LoadAgents must hand back an owned copy of each rung's Kinds")
	assert.Equal(t, "auto_accept", worker.Escalation[0].Action,
		"LoadAgents must hand back an owned copy of the Escalation slice itself")
}

// TestAgent_MutationDoesNotReachConfig covers the single-name lookup, which is
// the path operations.ResolveAgent and DefaultAgentProfiles actually take.
func TestAgent_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got, ok := cfg.Agent("worker")
	require.True(t, ok)
	require.NotEmpty(t, got.Profiles)
	got.Profiles[0] = "MUTATED"

	assert.Equal(t, []string{"p"}, cfg.GetConfiguredAgents()["worker"].Profiles,
		"Agent must hand back an owned copy of Profiles")
}

// LoadAgents warned on a directory-scan failure and then used the nil
// result as if it were a successful EMPTY scan, so the caller got a merged set
// silently missing every on-disk agent — and `run --agent dev` reported "no
// such agent" for an agent that is defined and merely unreadable.
//
// The row located the swallow one level too high. `agents.Loader.List`'s
// afero.Walk callback returned nil on EVERY error, so Walk (List's only error
// source) could never return non-nil and the config-side guard was unreachable
// by construction. The observable defect the row describes was real all the
// same; it just came out of List as (nil, nil) rather than as an error. Both
// halves are fixed: List distinguishes an unreadable DIRECTORY (whose whole
// subtree is invisible — reported) from an unreadable single ENTRY (warned and
// skipped, unchanged), and LoadAgents raises the result as a fatal-class
// finding the startup choke can abort on rather than a stderr line the run
// walks straight past.
func TestLoadAgents_UnreadableAgentsDirectoryIsAFinding(t *testing.T) {
	resetStrictness(t)
	mem := afero.NewMemMapFs()
	appPath := "/app"
	agentsDir := paths.AgentsPath(appPath)
	require.NoError(t, mem.MkdirAll(agentsDir, 0o755))
	require.NoError(t, afero.WriteFile(mem, filepath.Join(agentsDir, "dev.yaml"),
		[]byte("llm: claude-code\nprofiles: [go-developer]\n"), 0o644))

	// Sanity: with a readable directory the agent is found. Without this the
	// assertion below could pass because the fixture never defined an agent.
	readable := NewFixture(Fixture{AppPaths: []string{appPath}})
	readable.SetFS(mem)
	require.Len(t, readable.LoadAgents(), 1, "the fixture must define one on-disk agent")

	cfg := NewFixture(Fixture{AppPaths: []string{appPath}})
	cfg.SetFS(denyOpenFs{Fs: mem, deny: agentsDir})

	mark := strictness.Checkpoint()
	got := cfg.LoadAgents()

	assert.Empty(t, got, "the unreadable directory still yields no agents — that half is fault tolerance")
	findings := strictness.Since(mark)
	require.Len(t, findings, 1,
		"an unreadable agents directory must be indistinguishable from neither an absent one nor an empty one: it must record a finding")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class)
	assert.Contains(t, findings[0].Message, agentsDir, "the finding names the directory it could not read")
}

// Agent(name) re-runs LoadAgents' full two-source merge on every
// lookup, and LoadAgents re-emitted its shadowing warning each time. Config's
// Agent() sits on the path operations.ResolveAgent, DefaultAgentProfiles and
// `agent show` all take, so one command reaches it several times and a single
// shadowed agent printed the same line once per lookup.
//
// The warning is a one-per-process statement of fact about the user's config,
// not a per-lookup event, so it collapses to WarnOnce — the same treatment
// every other repeat-from-independent-callers diagnostic in this codebase gets
// (see FwarnOnce's doc, written for exactly this shape).
func TestLoadAgents_ShadowWarningIsEmittedOncePerProcess(t *testing.T) {
	mem := afero.NewMemMapFs()
	appPath := "/shadowprobe"
	agentsDir := paths.AgentsPath(appPath)
	require.NoError(t, mem.MkdirAll(agentsDir, 0o755))
	// A name unique to this test: WarnOnce keys on the rendered line and has
	// no process-wide reset, so a shared name could be pre-consumed by another
	// test and make this pass vacuously.
	const name = "u047f08probe"
	require.NoError(t, afero.WriteFile(mem, filepath.Join(agentsDir, name+".yaml"),
		[]byte("llm: claude-code\nprofiles: [from-disk]\n"), 0o644))

	cfg := NewFixture(Fixture{
		AppPaths: []string{appPath},
		Agents:   map[string]agents.Agent{name: {LLM: "claude-code", Profiles: []string{"from-config"}}},
	})
	cfg.SetFS(mem)

	var sink strings.Builder
	restore := clidiag.SetSink(&sink)
	defer restore()

	for range 3 {
		got, ok := cfg.Agent(name)
		require.True(t, ok)
		require.Equal(t, []string{"from-config"}, got.Profiles, "the config key must still win the collision")
	}

	assert.Equal(t, 1, strings.Count(sink.String(), name+"\" is defined in both"),
		"the shadowing warning states a fact about the config once; repeating it per lookup is noise the user cannot act on")
}

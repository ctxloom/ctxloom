package configload

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/config"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestLoad_EnginelessAgentIsRefusedNamingKeyAndPath is the row's first
// settling condition: `agents: x: {}` declares an agent that can resolve no
// engine — no llm, and no profile that could carry one — and that is not a
// degraded agent, it is not an agent. It must be refused at load with a
// finding naming the key and the file, and it must not reach the agents map,
// so nothing downstream (`agent list`, `run --agent x`, default_agent) can
// ever see a binding with nothing bound.
func TestLoad_EnginelessAgentIsRefusedNamingKeyAndPath(t *testing.T) {
	cfg := writeLayers(t, "", "version: 6\nagents:\n  x: {}\n  dev:\n    profiles: [default]\n")

	_, present := cfg.GetConfiguredAgents()["x"]
	assert.False(t, present, "an agent with no llm and no profiles must be dropped from the agents map, not carried as an empty binding")
	_, present = cfg.GetConfiguredAgents()["dev"]
	assert.True(t, present, "a sibling that CAN resolve an engine through its profiles is untouched")

	found := warningsOfKind(cfg, config.WarnKindEnginelessAgent)
	require.Len(t, found, 1, "exactly one finding for the one refused binding")
	assert.Contains(t, found[0], "agents.x", "the finding must name the key that was refused")
	assert.Contains(t, found[0], paths.ConfigPath("/proj/.ctxloom"), "the finding must name the file the binding came from")
	assert.NotContains(t, found[0], "agents.dev", "the sibling that passed is not named")

	for _, a := range cfg.LoadAgents() {
		assert.NotEqual(t, "x", a.Name, "LoadAgents must not surface the refused binding")
	}
}

// TestLoad_AgentWithProfilesButNoLLMIsAccepted guards the refusal's edge: an
// llm is documented optional (it falls back to the composed profiles' llm and
// then the project default), so an agent bound to profiles alone is a real
// agent and must keep loading exactly as before.
func TestLoad_AgentWithProfilesButNoLLMIsAccepted(t *testing.T) {
	cfg := writeLayers(t, "", "version: 6\nagents:\n  reviewer:\n    profiles: [cr-correctness]\n")

	got, ok := cfg.Agent("reviewer")
	require.True(t, ok)
	assert.Equal(t, []string{"cr-correctness"}, got.Profiles)
	assert.Empty(t, warningsOfKind(cfg, config.WarnKindEnginelessAgent))
}

// TestLoad_AgentWithLLMButNoProfilesIsAccepted is the other edge: an llm
// alone is a complete engine binding (the context is then the project
// default's), so it is not engineless either.
func TestLoad_AgentWithLLMButNoProfilesIsAccepted(t *testing.T) {
	cfg := writeLayers(t, "", "version: 6\nllm:\n  configs:\n    fast: {type: claude-code}\nagents:\n  quick:\n    llm: fast\n")

	got, ok := cfg.Agent("quick")
	require.True(t, ok)
	assert.Equal(t, "fast", got.LLM)
	assert.Empty(t, warningsOfKind(cfg, config.WarnKindEnginelessAgent))
}

// TestLoad_HomeEnginelessAgentIsRefusedNamingHomePath is the row's observed
// input. Every per-agent FIELD is ScopeShared, so a home agent that declares
// any field has it dropped by the layer-scope check — and koanf's Delete
// prunes the emptied parent, so such an agent never reaches the merge at
// all. A verbatim `help: {}` carries no field for that check to see: it
// survived into the merged view and was re-serialised into the project file
// by the next project-layer save. The refusal runs per layer so the finding
// names HOME's path — where the declaration actually lives — and drops the
// shell before the merge.
func TestLoad_HomeEnginelessAgentIsRefusedNamingHomePath(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	projectAppDir := seedLayers(t, fs, home,
		"version: 6\nagents:\n  help: {}\n",
		"version: 6\nagents:\n  dev:\n    profiles: [default]\n",
	)
	cfg, err := Load(WithFS(fs), WithAppDir(projectAppDir))
	require.NoError(t, err)

	_, present := cfg.GetConfiguredAgents()["help"]
	assert.False(t, present, "a home-only engineless agent must not reach the merged agents map")

	found := warningsOfKind(cfg, config.WarnKindEnginelessAgent)
	require.Len(t, found, 1)
	assert.Contains(t, found[0], "agents.help")
	assert.Contains(t, found[0], paths.ConfigPath(filepath.Join(home, config.AppDirName)),
		"the finding must name the HOME file, which is where the offending declaration lives")
	assert.NotContains(t, found[0], "/proj/", "the project file did not declare it and must not be blamed")
}

// TestManagerUpdate_ProjectWriteDoesNotFoldHomeAgentIntoProjectFile is the
// row's third settling condition, end to end: a project-layer `agent create`
// in a repo whose HOME config declares an extra agent must leave the project
// file without that agent. Owner.Update saves the MERGED view, so the only
// way a home-only agent stays out of the committed file is for it never to
// survive load in the first place.
func TestManagerUpdate_ProjectWriteDoesNotFoldHomeAgentIntoProjectFile(t *testing.T) {
	home := testsupport.Isolate(t)
	fs := afero.NewMemMapFs()
	homeAppDir := filepath.Join(home, config.AppDirName)
	projectAppDir := seedLayers(t, fs, home, "version: 6\nagents:\n  help: {}\n", "version: 6\n")

	mgr := newUpdater(t, WithFS(fs), WithAppDir(projectAppDir))
	require.NoError(t, mgr.Update(func(d *config.Draft) error {
		if d.Agents == nil {
			d.Agents = map[string]agents.Agent{}
		}
		d.Agents["dev"] = agents.Agent{Profiles: []string{"default"}}
		return nil
	}))

	written, err := afero.ReadFile(fs, paths.ConfigPath(projectAppDir))
	require.NoError(t, err)
	assert.NotContains(t, string(written), "help", "a home-only agent must not be re-serialised into the project file by a project-layer write")
	assert.Contains(t, string(written), "dev", "the agent the write actually created lands")

	homeAfter, err := afero.ReadFile(fs, paths.ConfigPath(homeAppDir))
	require.NoError(t, err)
	assert.Equal(t, "version: 6\nagents:\n  help: {}\n", string(homeAfter), "a project-layer write never touches the home file")
}

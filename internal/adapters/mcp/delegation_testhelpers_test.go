package mcp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// delegationFixture stages a hermetic project (one bundle fragment, one
// profile composing it, mock-typed engine labels) and the given agents, with
// HOME scrubbed so session accounting and trust stores stay in the sandbox.
//
// GAP 1 (prodSpawner.Resolve re-reads agent definitions FROM DISK per call —
// spawner.go's resolveCfg) means the returned in-memory cfg.GetConfiguredAgents() is no
// longer the only thing a spawn sees: a REAL config.yaml describing the SAME
// bindings must exist at app too, or the disk reload observes an empty
// agent set the in-memory cfg promised. writeDelegationConfigYAML keeps the
// two in lockstep.
func delegationFixture(t *testing.T, subs map[string]agents.Agent) (*config.Config, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	writeDelegationFile(t, filepath.Join(paths.LocalBundlesPathFor(app, paths.LayoutV2), "kit1.yaml"),
		"version: \"1.0.0\"\nfragments:\n  f1:\n    content: \"FRAG-ONE\"\n")
	writeDelegationFile(t, filepath.Join(app, "profiles", "p1.yaml"),
		"bundles:\n  - ctxloom:local@bundles/kit1\n")
	writeDelegationConfigYAML(t, app, subs)
	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{app},
		LM: config.LMConfig{
			Configs: map[string]config.LLMConfig{
				"fast": {Type: "mock", Body: map[string]any{"model": "m-fast"}},
			},
			Defaults: config.RoleDefaults{Primary: "fast"},
		},
		Agents: subs,
	})
	return cfg, root
}

// writeDelegationConfigYAML persists the fixture's LLM registry + agents to
// a real config.yaml, mirroring the in-memory cfg built alongside it (see
// delegationFixture's GAP 1 note).
func writeDelegationConfigYAML(t *testing.T, app string, subs map[string]agents.Agent) {
	t.Helper()
	doc := struct {
		Version int `yaml:"version"`
		LLM     struct {
			Configs map[string]struct {
				Type  string `yaml:"type"`
				Model string `yaml:"model"`
			} `yaml:"configs"`
			Defaults struct {
				Primary string `yaml:"primary"`
			} `yaml:"defaults"`
		} `yaml:"llm"`
		Agents map[string]agents.Agent `yaml:"agents,omitempty"`
	}{Version: config.CurrentConfigVersion}
	doc.LLM.Configs = map[string]struct {
		Type  string `yaml:"type"`
		Model string `yaml:"model"`
	}{"fast": {Type: "mock", Model: "m-fast"}}
	doc.LLM.Defaults.Primary = "fast"
	doc.Agents = subs
	raw, err := yaml.Marshal(doc)
	require.NoError(t, err)
	writeDelegationFile(t, filepath.Join(app, "config.yaml"), string(raw))
}

func writeDelegationFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

func headlessAgent(profiles ...string) agents.Agent {
	return agents.Agent{LLM: "fast", Profiles: profiles, Permissions: "bypass"}
}

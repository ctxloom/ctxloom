package config

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agents"
)

type characterizationCase struct {
	name string
	cfg  func() *Config
}

type characterizationOutput struct {
	name   string
	render func(*Config) ([]byte, error)
}

// characterizationOutputs are the three byte-producing paths over one Config:
// Marshal (init's scaffold write), yaml.Marshal(cfg) through MarshalYAML
// (`config show`), and saveLocked's first write (Owner.Update) under each
// layer source, since SourceProject applies the layer-scope filter and
// SourceHome does not.
func characterizationOutputs() []characterizationOutput {
	save := func(src ConfigSource) func(*Config) ([]byte, error) {
		return func(c *Config) ([]byte, error) {
			c.source = src
			fs := afero.NewMemMapFs()
			if err := c.saveLocked(fs, "/config.yaml"); err != nil {
				return nil, err
			}
			return afero.ReadFile(fs, "/config.yaml")
		}
	}
	return []characterizationOutput{
		{"Marshal", func(c *Config) ([]byte, error) { return c.Marshal() }},
		{"MarshalYAML", func(c *Config) ([]byte, error) { return yaml.Marshal(c) }},
		{"saveLocked-project", save(SourceProject)},
		{"saveLocked-home", save(SourceHome)},
	}
}

func characterizationCases() []characterizationCase {
	return []characterizationCase{
		{"empty", func() *Config { return NewFixture(Fixture{}) }},
		{"full", func() *Config {
			f := fullyPopulatedFixture()
			f.Delegation.IdleTimeout = "10m"
			f.LM.Defaults = RoleDefaults{Primary: "fast", Fast: "fast"}
			f.LM.Configs["fast"] = LLMConfig{Type: "claude-code", Role: "fast", Permissions: "plan", Body: map[string]any{"model": "m1"}}
			f.Editor.Args = []string{"-n"}
			f.Agents["worker"] = agents.Agent{LLM: "fast"}
			return NewFixture(f)
		}},
		{"explicit_false_and_stale_version", func() *Config {
			no := false
			return NewFixture(Fixture{
				Version:                   1,
				Sync:                      SyncConfig{AutoSync: &no},
				IsolationDevcontainerBase: &no,
				UI:                        UIConfig{Surround: &no},
			})
		}},
		{"default_overlay", func() *Config {
			c := NewFixture(Fixture{
				Version: CurrentConfigVersion,
				LM: LMConfig{
					Configs:  map[string]LLMConfig{"shipped": {Type: "claude-code", Role: "primary"}, "mine": {Type: "codex"}},
					Defaults: RoleDefaults{Primary: "shipped"},
				},
			})
			c.lmDefaultOverlay = &LMConfig{
				Configs:  map[string]LLMConfig{"shipped": {Type: "claude-code", Role: "primary"}},
				Defaults: RoleDefaults{Primary: "shipped"},
			}
			return c
		}},
	}
}

// TestConfigSerializers_Characterization pins today's exact bytes from every
// path that renders a Config. It is the precondition for consolidating
// Marshal/applyConfigSections and MarshalYAML/toDoc (errant-john): those paths
// do NOT agree today — version stamping, overlay and role stripping, pruning
// and key order all differ by design — so any consolidation that moves a byte
// here is a change to the on-disk format or to `config show`, not a refactor.
// Regenerate only as a deliberate format decision.
func TestConfigSerializers_Characterization(t *testing.T) {
	seen := 0
	for _, tc := range characterizationCases() {
		for _, out := range characterizationOutputs() {
			key := tc.name + "/" + out.name
			t.Run(key, func(t *testing.T) {
				want, ok := characterizationGolden[key]
				if !assert.Truef(t, ok, "no golden for %s", key) {
					return
				}
				got, err := out.render(tc.cfg())
				if assert.NoError(t, err) {
					assert.Equal(t, want, string(got))
				}
			})
			seen++
		}
	}
	assert.Len(t, characterizationGolden, seen, "a golden entry no case renders is pinning nothing")
}

var characterizationGolden = map[string]string{
	"empty/Marshal": `version: 6
`,
	"empty/MarshalYAML": `version: 0
llm: {}
`,
	"empty/saveLocked-project": `version: 6
`,
	"empty/saveLocked-home": `version: 6
`,
	"full/Marshal": `agents:
    worker:
        llm: fast
config:
    essence_max_chars: 4096
default_agent: worker
delegation:
    concurrency: 7
    depth: 2
    idle_timeout: 10m
dirty_tree_handler: commit
editor:
    command: vi
    args:
        - -n
isolation_base_containerfile: Containerfile.base
isolation_devcontainer_base: true
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
isolation_images:
    claude-code: example.invalid/img:tag
llm:
    configs:
        fast:
            type: claude-code
            permissions: plan
            model: m1
    defaults:
        primary: fast
        fast: fast
permissions: plan
runtime: container
session_purge_age: 180d
session_reap_age: 45d
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
version: 6
workspace: worktree
`,
	"full/MarshalYAML": `version: 6
llm:
    configs:
        fast:
            type: claude-code
            role: fast
            permissions: plan
            model: m1
    defaults:
        primary: fast
        fast: fast
editor:
    command: vi
    args:
        - -n
config:
    essence_max_chars: 4096
sync:
    auto_sync: true
agents:
    worker:
        llm: fast
default_agent: worker
workspace: worktree
dirty_tree_handler: commit
runtime: container
permissions: plan
delegation:
    concurrency: 7
    depth: 2
    idle_timeout: 10m
isolation_images:
    claude-code: example.invalid/img:tag
isolation_base_containerfile: Containerfile.base
isolation_devcontainer_base: true
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
ui:
    prefix_key: ctrl-]
    surround: true
session_reap_age: 45d
session_purge_age: 180d
`,
	"full/saveLocked-project": `agents:
    worker:
        llm: fast
config:
    essence_max_chars: 4096
default_agent: worker
dirty_tree_handler: commit
isolation_base_containerfile: Containerfile.base
llm:
    configs:
        fast:
            model: m1
            permissions: plan
            type: claude-code
    defaults:
        fast: fast
        primary: fast
permissions: plan
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
version: 6
workspace: worktree
`,
	"full/saveLocked-home": `agents:
    worker:
        llm: fast
config:
    essence_max_chars: 4096
default_agent: worker
delegation:
    concurrency: 7
    depth: 2
    idle_timeout: 10m
dirty_tree_handler: commit
editor:
    args:
        - -n
    command: vi
isolation_base_containerfile: Containerfile.base
isolation_devcontainer_base: true
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
isolation_images:
    claude-code: example.invalid/img:tag
llm:
    configs:
        fast:
            model: m1
            permissions: plan
            type: claude-code
    defaults:
        fast: fast
        primary: fast
permissions: plan
runtime: container
session_purge_age: 180d
session_reap_age: 45d
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
version: 6
workspace: worktree
`,
	"explicit_false_and_stale_version/Marshal": `isolation_devcontainer_base: false
sync:
    auto_sync: false
ui:
    surround: false
version: 6
`,
	"explicit_false_and_stale_version/MarshalYAML": `version: 1
llm: {}
sync:
    auto_sync: false
isolation_devcontainer_base: false
ui:
    surround: false
`,
	"explicit_false_and_stale_version/saveLocked-project": `sync:
    auto_sync: false
ui:
    surround: false
version: 6
`,
	"explicit_false_and_stale_version/saveLocked-home": `isolation_devcontainer_base: false
sync:
    auto_sync: false
ui:
    surround: false
version: 6
`,
	"default_overlay/Marshal": `llm:
    configs:
        mine:
            type: codex
version: 6
`,
	"default_overlay/MarshalYAML": `version: 6
llm:
    configs:
        mine:
            type: codex
        shipped:
            type: claude-code
            role: primary
    defaults:
        primary: shipped
`,
	"default_overlay/saveLocked-project": `llm:
    configs:
        mine:
            type: codex
version: 6
`,
	"default_overlay/saveLocked-home": `llm:
    configs:
        mine:
            type: codex
version: 6
`,
}

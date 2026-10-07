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

// characterizationOutputs are the byte-producing paths over one Config:
// yaml.Marshal(cfg) through MarshalYAML (`config show`, the effective
// document), yaml.Marshal(cfg.Authored()) (`config show --raw` and init's
// scaffold write), and saveLocked's first write (Owner.Update).
func characterizationOutputs() []characterizationOutput {
	save := func(c *Config) ([]byte, error) {
		fs := afero.NewMemMapFs()
		if err := c.saveLocked(fs, "/config.yaml"); err != nil {
			return nil, err
		}
		return afero.ReadFile(fs, "/config.yaml")
	}
	return []characterizationOutput{
		{"MarshalYAML", func(c *Config) ([]byte, error) { return yaml.Marshal(c) }},
		{"authored", func(c *Config) ([]byte, error) { return yaml.Marshal(c.Authored()) }},
		{"saveLocked", save},
	}
}

func characterizationCases() []characterizationCase {
	return []characterizationCase{
		{"empty", func() *Config { return NewFixture(Fixture{}) }},
		{"full", func() *Config {
			f := fullyPopulatedFixture()
			f.Delegation.IdleTimeout = "10m"
			f.LM.Defaults = RoleDefaults{Primary: "fast", Fast: "fast"}
			f.LM.Configs["fast"] = LLMConfig{Type: "claude-code", Role: "fast", Permissions: agents.LabelPermissions{Engine: map[string]any{"mode": "plan"}}, Body: map[string]any{"model": "m1"}}
			f.Editor.Args = []string{"-n"}
			f.Agents["worker"] = agents.Agent{LLM: "fast"}
			return NewFixture(f)
		}},
		{"explicit_false_and_stale_version", func() *Config {
			no := false
			return NewFixture(Fixture{
				SchemaVersion: 1,
				Sync:          SyncConfig{AutoSync: &no},
				UI:            UIConfig{Surround: &no},
			})
		}},
		{"default_overlay", func() *Config {
			c := NewFixture(Fixture{
				SchemaVersion: CurrentConfigVersion,
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

// TestConfigSerializers_Characterization pins the exact bytes of every path
// that renders a Config. They share one serializer (configDoc.MarshalYAML):
// the document is lossless, role included, the version is stamped current and
// keys are sorted at every depth. The effective view (MarshalYAML) carries the
// shipped default registry; the authored view and every save leave it out. Any
// change here is a change to the on-disk format or to `config show`, not a
// refactor. Regenerate only as a deliberate format decision.
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

// TestConfigSerializers_FirstSaveMatchesRender states the convergence the
// goldens only imply: a first save writes exactly the
// bytes init writes and `config show --raw` prints.
func TestConfigSerializers_FirstSaveMatchesRender(t *testing.T) {
	outs := characterizationOutputs()
	var render, save func(*Config) ([]byte, error)
	for _, o := range outs {
		switch o.name {
		case "authored":
			render = o.render
		case "saveLocked":
			save = o.render
		}
	}
	for _, tc := range characterizationCases() {
		t.Run(tc.name, func(t *testing.T) {
			want, err := render(tc.cfg())
			if !assert.NoError(t, err) {
				return
			}
			got, err := save(tc.cfg())
			if assert.NoError(t, err) {
				assert.Equal(t, string(want), string(got))
			}
		})
	}
}

var characterizationGolden = map[string]string{
	"empty/MarshalYAML": `schema_version: 7
`,
	"empty/authored": `schema_version: 7
`,
	"empty/saveLocked": `schema_version: 7
`,
	"full/MarshalYAML": `agents:
    worker:
        llm: fast
auth: login
companions:
    - acme
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
isolation_base: devcontainer
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
isolation_images:
    claude-code: example.invalid/img:tag
llm:
    configs:
        fast:
            model: m1
            permissions:
                mode: plan
            role: fast
            type: claude-code
    defaults:
        fast: fast
        primary: fast
output_dir: /data/ctxloom-outputs
permissions:
    approver: none
    network: false
runtime: container
schema_version: 7
session_purge_age: 180d
session_reap_age: 45d
shell_timeout:
    default: 3m
    max: 90m
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
workspace: worktree
`,
	"full/authored": `agents:
    worker:
        llm: fast
auth: login
companions:
    - acme
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
isolation_base: devcontainer
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
isolation_images:
    claude-code: example.invalid/img:tag
llm:
    configs:
        fast:
            model: m1
            permissions:
                mode: plan
            role: fast
            type: claude-code
    defaults:
        fast: fast
        primary: fast
output_dir: /data/ctxloom-outputs
permissions:
    approver: none
    network: false
runtime: container
schema_version: 7
session_purge_age: 180d
session_reap_age: 45d
shell_timeout:
    default: 3m
    max: 90m
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
workspace: worktree
`,
	"full/saveLocked": `agents:
    worker:
        llm: fast
auth: login
companions:
    - acme
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
isolation_base: devcontainer
isolation_devcontainer_service: app
isolation_engines:
    - claude-code
isolation_images:
    claude-code: example.invalid/img:tag
llm:
    configs:
        fast:
            model: m1
            permissions:
                mode: plan
            role: fast
            type: claude-code
    defaults:
        fast: fast
        primary: fast
output_dir: /data/ctxloom-outputs
permissions:
    approver: none
    network: false
runtime: container
schema_version: 7
session_purge_age: 180d
session_reap_age: 45d
shell_timeout:
    default: 3m
    max: 90m
sync:
    auto_sync: true
ui:
    prefix_key: ctrl-]
    surround: true
workspace: worktree
`,
	"explicit_false_and_stale_version/MarshalYAML": `schema_version: 7
sync:
    auto_sync: false
ui:
    surround: false
`,
	"explicit_false_and_stale_version/authored": `schema_version: 7
sync:
    auto_sync: false
ui:
    surround: false
`,
	"explicit_false_and_stale_version/saveLocked": `schema_version: 7
sync:
    auto_sync: false
ui:
    surround: false
`,
	"default_overlay/MarshalYAML": `llm:
    configs:
        mine:
            type: codex
        shipped:
            role: primary
            type: claude-code
    defaults:
        primary: shipped
schema_version: 7
`,
	"default_overlay/authored": `llm:
    configs:
        mine:
            type: codex
schema_version: 7
`,
	"default_overlay/saveLocked": `llm:
    configs:
        mine:
            type: codex
schema_version: 7
`,
}

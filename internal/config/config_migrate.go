package config

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
const versionKey = "version"

// CurrentConfigVersion is the config *schema* version ctxloom writes and
// upgrades toward. It is an integer, deliberately distinct from the application
// version (cmd.Version, a string like "v0.6.4"). Bump it whenever a new Upgrader
// is appended to configUpgrades. A config with no `version` is treated as the
// pre-versioning generation (version 0/1) and is upgraded on load.
const CurrentConfigVersion = 2

// llmRenameUpgrade is the v1→v2 config upgrade: the schema generation that
// renamed the "plugin" abstraction to "llm". It rewrites pre-rename keys so
// configs written before the rename load correctly:
//
//   - defaults.llm_plugin: X  → llm.default: X
//   - llm.plugins.<name>: {…} → llm.configs.<name>: {…}
//
// It mutates the YAML node tree (via the shared upgrade helpers) so comments and
// key order survive, then stamps version 2.
type llmRenameUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (llmRenameUpgrade) Name() string { return "plugin→llm rename (v1→v2)" }

// Apply performs the rename and stamps version 2. It is a no-op once the
// document is already at version 2+; stamping the version is itself a valid
// upgrade, so an already-key-correct but unversioned config upgrades simply by
// gaining `version: 2`.
func (llmRenameUpgrade) Apply(root *yaml.Node) (changed bool) {
	if upgrade.Version(root, versionKey) >= 2 {
		return false
	}

	// defaults.llm_plugin → llm.default (don't clobber an explicit llm.default)
	if defaults := upgrade.MapValue(root, "defaults"); defaults != nil && defaults.Kind == yaml.MappingNode {
		if v := upgrade.MapValue(defaults, "llm_plugin"); v != nil {
			if v.Kind == yaml.ScalarNode && v.Value != "" {
				llm := upgrade.EnsureMap(root, "llm")
				if upgrade.MapValue(llm, "default") == nil {
					upgrade.MapSet(llm, "default", upgrade.ScalarNode(v.Value))
				}
			}
			upgrade.MapDelete(defaults, "llm_plugin")
			if len(defaults.Content) == 0 {
				upgrade.MapDelete(root, "defaults")
			}
		}
	}

	// llm.plugins.* → llm.configs.* (don't clobber an existing configs entry)
	if llm := upgrade.MapValue(root, "llm"); llm != nil && llm.Kind == yaml.MappingNode {
		if plugins := upgrade.MapValue(llm, "plugins"); plugins != nil && plugins.Kind == yaml.MappingNode {
			configs := upgrade.EnsureMap(llm, "configs")
			for i := 0; i+1 < len(plugins.Content); i += 2 {
				name := plugins.Content[i]
				if upgrade.MapValue(configs, name.Value) == nil {
					configs.Content = append(configs.Content, name, plugins.Content[i+1])
				}
			}
			upgrade.MapDelete(llm, "plugins")
		}
	}

	upgrade.SetVersion(root, versionKey, 2)
	return true
}

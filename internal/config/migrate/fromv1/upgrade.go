// Package fromv1 carries every schema migration OFF config version 1, together
// with the tests that prove it.
//
// v1 configs: the generation that spelled the LLM abstraction "plugin".
//
// The directory is the unit of support: when v1 configs stop being supported,
// this whole package is deleted and one line leaves config's pipeline. Nothing
// else has to be untangled, which is the entire reason the migrations are keyed
// by SOURCE version rather than by what they migrate toward.
package fromv1

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
// Declared per package so this directory stays independently deletable.
const versionKey = "version"

// Upgrade is the v1→v2 config upgrade: the schema generation that
// renamed the "plugin" abstraction to "llm". It rewrites pre-rename keys so
// configs written before the rename load correctly:
//
//   - defaults.llm_plugin: X  → llm.default: X
//   - llm.plugins.<name>: {…} → llm.configs.<name>: {…}
//
// It mutates the YAML node tree (via the shared upgrade helpers) so comments and
// key order survive, then stamps version 2.
type Upgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (Upgrade) Name() string { return "plugin→llm rename (v1→v2)" }

// Apply performs the rename and stamps version 2. It is a no-op once the
// document is already at version 2+; stamping the version is itself a valid
// upgrade, so an already-key-correct but unversioned config upgrades simply by
// gaining `version: 2`.
func (Upgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 2 {
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

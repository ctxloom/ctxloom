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
const CurrentConfigVersion = 3

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

// labeledConfigUpgrade is the v2→v3 config upgrade: it reshapes the flat,
// backend-keyed LLM config into the labeled-config + role-map form, and folds
// the old top-level defaults bag and root profiles map into their new homes.
// It is a comment-preserving yaml.Node rewrite (via the shared upgrade helpers)
// so key order and comments survive. The moves:
//
//   - llm.configs.<backend>: add `type: <backend>` (key was the backend; now a
//     label, so the type discriminator carries the backend identity).
//   - llm.default: X        → llm.defaults.primary: X
//   - llm.compaction.llm    → llm.defaults.fast (else fast points at primary)
//   - llm.compaction.model  → folded onto the fast label's `model`
//   - llm.compaction.chunks → config.compaction_chunks
//   - defaults.profiles     → profiles.defaults
//   - root profiles:        → profiles.definitions
//   - defaults.use_distilled → config.use_distilled
//   - delete defaults:, llm.compaction; set version 3.
type labeledConfigUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (labeledConfigUpgrade) Name() string { return "labeled LLM configs + role map (v2→v3)" }

// Apply performs the reshape and stamps version 3, a no-op once at version 3+.
func (labeledConfigUpgrade) Apply(root *yaml.Node) (changed bool) {
	if upgrade.Version(root, versionKey) >= 3 {
		return false
	}

	migrateLLMv3(root)
	migrateProfilesV3(root)
	migrateSettingsV3(root)

	if defaults := upgrade.MapValue(root, "defaults"); defaults != nil && defaults.Kind == yaml.MappingNode && len(defaults.Content) == 0 {
		upgrade.MapDelete(root, "defaults")
	}

	upgrade.SetVersion(root, versionKey, 3)
	return true
}

// migrateLLMv3 stamps `type` onto each labeled config, renames llm.default to
// llm.defaults.primary, and folds llm.compaction into the fast role.
func migrateLLMv3(root *yaml.Node) {
	llm := upgrade.MapValue(root, "llm")
	if llm == nil || llm.Kind != yaml.MappingNode {
		return
	}

	// Each existing configs.<key> gains `type: <key>` (key was the backend).
	if configs := upgrade.MapValue(llm, "configs"); configs != nil && configs.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(configs.Content); i += 2 {
			label := configs.Content[i]
			entry := configs.Content[i+1]
			if entry.Kind != yaml.MappingNode {
				continue
			}
			if upgrade.MapValue(entry, "type") == nil {
				// Prepend type so it reads as the discriminator first.
				entry.Content = append([]*yaml.Node{
					upgrade.ScalarNode("type"), upgrade.ScalarNode(label.Value),
				}, entry.Content...)
			}
		}
	}

	roleDefaults := upgrade.EnsureMap(llm, "defaults")

	// llm.default → llm.defaults.primary
	if def := upgrade.MapValue(llm, "default"); def != nil && def.Kind == yaml.ScalarNode && def.Value != "" {
		if upgrade.MapValue(roleDefaults, "primary") == nil {
			upgrade.MapSet(roleDefaults, "primary", upgrade.ScalarNode(def.Value))
		}
		upgrade.MapDelete(llm, "default")
	}

	// llm.compaction.{llm,model,chunks}
	if comp := upgrade.MapValue(llm, "compaction"); comp != nil && comp.Kind == yaml.MappingNode {
		fastLabel := ""
		if cl := upgrade.MapValue(comp, "llm"); cl != nil && cl.Kind == yaml.ScalarNode {
			fastLabel = cl.Value
		}
		// Fall back to the primary label so the fast role still resolves.
		if fastLabel == "" {
			if p := upgrade.MapValue(roleDefaults, "primary"); p != nil && p.Kind == yaml.ScalarNode {
				fastLabel = p.Value
			}
		}
		if fastLabel != "" && upgrade.MapValue(roleDefaults, "fast") == nil {
			upgrade.MapSet(roleDefaults, "fast", upgrade.ScalarNode(fastLabel))
		}
		// compaction.model folds onto the fast label's config model. The
		// explicit compaction model is the compression model the user chose, so
		// it wins over any model already on that label.
		if cm := upgrade.MapValue(comp, "model"); cm != nil && cm.Kind == yaml.ScalarNode && cm.Value != "" && fastLabel != "" {
			configs := upgrade.EnsureMap(llm, "configs")
			entry := upgrade.EnsureMap(configs, fastLabel)
			if upgrade.MapValue(entry, "type") == nil {
				entry.Content = append([]*yaml.Node{
					upgrade.ScalarNode("type"), upgrade.ScalarNode(fastLabel),
				}, entry.Content...)
			}
			upgrade.MapSet(entry, "model", upgrade.ScalarNode(cm.Value))
		}
		// compaction.chunks → config.compaction_chunks
		if ch := upgrade.MapValue(comp, "chunks"); ch != nil && ch.Kind == yaml.ScalarNode && ch.Value != "" {
			settings := upgrade.EnsureMap(root, "config")
			if upgrade.MapValue(settings, "compaction_chunks") == nil {
				node := upgrade.ScalarNode(ch.Value)
				node.Tag = "!!int"
				upgrade.MapSet(settings, "compaction_chunks", node)
			}
		}
		upgrade.MapDelete(llm, "compaction")
	}

	// Drop an empty role-defaults map so we don't leave `defaults: {}` behind.
	if len(roleDefaults.Content) == 0 {
		upgrade.MapDelete(llm, "defaults")
	}
}

// migrateProfilesV3 moves defaults.profiles → profiles.defaults and the root
// profiles map → profiles.definitions.
func migrateProfilesV3(root *yaml.Node) {
	// Capture the legacy root profiles map (a definitions map) before we
	// overwrite the `profiles` key with the new ProfilesConfig shape. A v2
	// `profiles:` is a mapping of name→profile; the v3 ProfilesConfig has
	// `defaults`/`definitions`. Detect the legacy shape by absence of those keys.
	var legacyDefs *yaml.Node
	if prof := upgrade.MapValue(root, "profiles"); prof != nil && prof.Kind == yaml.MappingNode {
		if upgrade.MapValue(prof, "definitions") == nil && upgrade.MapValue(prof, "defaults") == nil && len(prof.Content) > 0 {
			legacyDefs = prof
		}
	}

	var defaultsList *yaml.Node
	if defaults := upgrade.MapValue(root, "defaults"); defaults != nil && defaults.Kind == yaml.MappingNode {
		if p := upgrade.MapValue(defaults, "profiles"); p != nil {
			defaultsList = p
			upgrade.MapDelete(defaults, "profiles")
		}
	}

	if legacyDefs == nil && defaultsList == nil {
		return
	}

	newProfiles := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	if defaultsList != nil {
		upgrade.MapSet(newProfiles, "defaults", defaultsList)
	}
	if legacyDefs != nil {
		upgrade.MapSet(newProfiles, "definitions", legacyDefs)
	}
	upgrade.MapSet(root, "profiles", newProfiles)
}

// mapEntry returns the key and value nodes for key in a mapping node (both nil
// if absent). Unlike upgrade.MapValue it exposes the key node so a relocation
// can carry the key's comments along.
func mapEntry(m *yaml.Node, key string) (keyNode, valueNode *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// migrateSettingsV3 moves defaults.use_distilled → config.use_distilled.
func migrateSettingsV3(root *yaml.Node) {
	defaults := upgrade.MapValue(root, "defaults")
	if defaults == nil || defaults.Kind != yaml.MappingNode {
		return
	}
	if keyNode, ud := mapEntry(defaults, "use_distilled"); ud != nil {
		settings := upgrade.EnsureMap(root, "config")
		if upgrade.MapValue(settings, "use_distilled") == nil {
			// Move the original key node too, so its head/line comments ride
			// along (MapSet's synthesized key would drop them).
			settings.Content = append(settings.Content, keyNode, ud)
		}
		upgrade.MapDelete(defaults, "use_distilled")
	}
}

// Package fromv2 carries every schema migration OFF config version 2, together
// with the tests that prove it.
//
// v2 configs: unlabeled LLM config with no role map.
//
// The directory is the unit of support: when v2 configs stop being supported,
// this whole package is deleted and one line leaves config's pipeline. Nothing
// else has to be untangled, which is the entire reason the migrations are keyed
// by SOURCE version rather than by what they migrate toward.
package fromv2

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
// Declared per package so this directory stays independently deletable.
const versionKey = "version"

// reportLoss sends a dropped-setting diagnostic to report when the caller
// supplied one. A nil Reporter means nobody is collecting, which is legal — so
// every loss goes through here rather than calling the callback directly.
func reportLoss(report upgrade.Reporter, format string, args ...any) {
	if report == nil {
		return
	}
	report(format, args...)
}

// Upgrade is the v2→v3 config upgrade: it reshapes the flat,
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
type Upgrade struct{ Report upgrade.Reporter }

// Name identifies the upgrade in logs and the rewrite prompt.
func (Upgrade) Name() string { return "labeled LLM configs + role map (v2→v3)" }

// Apply performs the reshape and stamps version 3, a no-op once at version 3+.
func (u Upgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 3 {
		return false
	}

	migrateLLMv3(root, u.Report)
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
func migrateLLMv3(root *yaml.Node, report upgrade.Reporter) {
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
		if cm := upgrade.MapValue(comp, "model"); cm != nil && cm.Kind == yaml.ScalarNode && cm.Value != "" {
			if fastLabel != "" {
				configs := upgrade.EnsureMap(llm, "configs")
				entry := upgrade.EnsureMap(configs, fastLabel)
				if upgrade.MapValue(entry, "type") == nil {
					entry.Content = append([]*yaml.Node{
						upgrade.ScalarNode("type"), upgrade.ScalarNode(fastLabel),
					}, entry.Content...)
				}
				upgrade.MapSet(entry, "model", upgrade.ScalarNode(cm.Value))
			} else {
				// No compaction.llm and no primary label means there is no LLM
				// label to attach the model to. Record the loss (surfaced as a
				// migration-lossy config warning; fatal in strict mode) rather
				// than silently drop the user's chosen compaction model — this
				// migration is irreversible on disk.
				reportLoss(report, "config migration: dropped compaction model %q (no LLM label to attach it to); set llm.defaults.fast and re-specify the model", cm.Value)
			}
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

// migrateSettingsV3 moves defaults.use_distilled → config.use_distilled.
func migrateSettingsV3(root *yaml.Node) {
	defaults := upgrade.MapValue(root, "defaults")
	if defaults == nil || defaults.Kind != yaml.MappingNode {
		return
	}
	if keyNode, ud := upgrade.MapEntry(defaults, "use_distilled"); ud != nil {
		settings := upgrade.EnsureMap(root, "config")
		if upgrade.MapValue(settings, "use_distilled") == nil {
			// Move the original key node too, so its head/line comments ride
			// along (MapSet's synthesized key would drop them).
			settings.Content = append(settings.Content, keyNode, ud)
		}
		upgrade.MapDelete(defaults, "use_distilled")
	}
}

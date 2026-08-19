// Package fromv3 carries every schema migration OFF config version 3, together
// with the tests that prove it.
//
// v3 configs: the generation carrying a `gemini` backend key.
//
// The directory is the unit of support: when v3 configs stop being supported,
// this whole package is deleted and one line leaves config's pipeline. Nothing
// else has to be untangled, which is the entire reason the migrations are keyed
// by SOURCE version rather than by what they migrate toward.
package fromv3

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

// Upgrade is the v3→v4 config upgrade: the "gemini" backend
// was removed and replaced by "antigravity" (the Antigravity CLI, binary agy).
// It is a comment-preserving yaml.Node rewrite (via the shared upgrade helpers).
// The moves:
//
//   - llm.configs.* with `type: gemini` → `type: antigravity`. The gemini-only
//     knobs trust_workspace and approval_mode have no antigravity equivalent
//     (they are schema-invalid now) and are dropped. binary_path pointed at the
//     gemini binary, which antigravity cannot run, so it is dropped too — the
//     default agy binary resolution is correct. model/args/env are kept: a
//     stale gemini model name is still schema-valid and the user's to update.
//   - hooks.plugins.gemini → hooks.plugins.antigravity (key renamed in place;
//     when an antigravity block already exists the dead gemini block is dropped
//     rather than clobbering it — the gemini backend it targeted is gone).
//   - mcp.plugins.gemini → mcp.plugins.antigravity, same rule.
//
// Labels (llm.configs keys, llm.defaults role references) are never touched: a
// label named "gemini" is just a name; only the type discriminator matters.
type Upgrade struct{ Report upgrade.Reporter }

// Name identifies the upgrade in logs and the rewrite prompt.
func (Upgrade) Name() string { return "gemini→antigravity backend (v3→v4)" }

// Apply performs the replacement and stamps version 4, a no-op once at
// version 4+. As with earlier steps, stamping the version is itself a valid
// upgrade, so a gemini-free v3 config upgrades simply by gaining `version: 4`.
func (u Upgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 4 {
		return false
	}

	// llm.configs entries typed gemini flip to antigravity and shed the
	// gemini-only fields.
	if llm := upgrade.MapValue(root, "llm"); llm != nil && llm.Kind == yaml.MappingNode {
		if configs := upgrade.MapValue(llm, "configs"); configs != nil && configs.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(configs.Content); i += 2 {
				label := configs.Content[i]
				entry := configs.Content[i+1]
				if entry.Kind != yaml.MappingNode {
					continue
				}
				typ := upgrade.MapValue(entry, "type")
				if typ == nil || typ.Kind != yaml.ScalarNode || typ.Value != "gemini" {
					continue
				}
				// Rewrite the scalar in place so the node's comments survive.
				typ.Value = "antigravity"
				// These three gemini-only knobs have no antigravity equivalent and
				// are dropped — but a USER-SET value being deleted by a migration
				// is an irreversible on-disk loss, so name each one the way
				// migrateLLMv3 names its own lossy branch (recordMigrationWarning →
				// WarnKindMigrationLossy, fatal in strict mode) instead of dropping
				// it silently (U049-F18).
				for _, key := range []string{"trust_workspace", "approval_mode", "binary_path"} {
					if v := upgrade.MapValue(entry, key); v != nil && v.Kind == yaml.ScalarNode {
						reportLoss(u.Report, "config migration (gemini→antigravity): dropped %s=%q from llm config %q; it has no antigravity equivalent", key, v.Value, label.Value)
					}
					upgrade.MapDelete(entry, key)
				}
			}
		}
	}

	// hooks.plugins.gemini / mcp.plugins.gemini follow the backend rename.
	for _, section := range []string{"hooks", "mcp"} {
		sec := upgrade.MapValue(root, section)
		if sec == nil || sec.Kind != yaml.MappingNode {
			continue
		}
		plugins := upgrade.MapValue(sec, "plugins")
		if plugins == nil || plugins.Kind != yaml.MappingNode {
			continue
		}
		keyNode, _ := upgrade.MapEntry(plugins, "gemini")
		if keyNode == nil {
			continue
		}
		if upgrade.MapValue(plugins, "antigravity") != nil {
			// An antigravity block already exists; the gemini block targeted a
			// backend that no longer exists, so it is dead — drop, don't merge.
			upgrade.MapDelete(plugins, "gemini")
		} else {
			// Rename the key node in place so its comments ride along.
			keyNode.Value = "antigravity"
		}
	}

	upgrade.SetVersion(root, versionKey, 4)
	return true
}

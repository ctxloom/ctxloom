package config

import (
	"fmt"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
const versionKey = "version"

// migrationWarnings collects lossy-migration diagnostics raised while the
// upgrade pipeline runs. The Upgrader interface has no warning channel and no
// access to the Config being built, so lossy steps record here and
// loadConfigFile drains into cfg.warnings (kind migration-lossy) right after
// configUpgrades.Run — the pipeline's single call site — so the strict startup
// gate can abort on a silently dropped setting instead of it scrolling past as
// an unclassified stderr line.
var (
	migrationWarnMu   sync.Mutex
	migrationWarnings []string
)

// recordMigrationWarning notes a lossy migration (a user-set value the upgrade
// had to drop). The message must name the key to fix.
func recordMigrationWarning(format string, args ...any) {
	migrationWarnMu.Lock()
	defer migrationWarnMu.Unlock()
	migrationWarnings = append(migrationWarnings, fmt.Sprintf(format, args...))
}

// drainMigrationWarnings returns and clears the collected lossy-migration
// diagnostics.
func drainMigrationWarnings() []string {
	migrationWarnMu.Lock()
	defer migrationWarnMu.Unlock()
	out := migrationWarnings
	migrationWarnings = nil
	return out
}

// CurrentConfigVersion is the config *schema* version ctxloom writes and
// upgrades toward. It is an integer, deliberately distinct from the application
// version (cmd.Version, a string like "v0.6.4"). Bump it whenever a new Upgrader
// is appended to configUpgrades. A config with no `version` is treated as the
// pre-versioning generation (version 0/1) and is upgraded on load.
const CurrentConfigVersion = 6

// configUpgrades is the canonical, ordered set of config schema upgrades,
// oldest-first. Append an Upgrader here as the schema evolves and bump
// CurrentConfigVersion (immediately above) to match. The loader runs this
// pipeline over the raw config bytes on every load (see loadConfigFile);
// upgrades apply in memory and are only persisted after the user confirms
// (see Config.CommitUpgrade).
var configUpgrades = upgrade.Pipeline{
	llmRenameUpgrade{},
	labeledConfigUpgrade{},
	geminiToAntigravityUpgrade{},
	profileCommandSelectorUpgrade{},
	defaultAgentUpgrade{},
}

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
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 3 {
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
				recordMigrationWarning("config migration: dropped compaction model %q (no LLM label to attach it to); set llm.defaults.fast and re-specify the model", cm.Value)
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

// geminiToAntigravityUpgrade is the v3→v4 config upgrade: the "gemini" backend
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
type geminiToAntigravityUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (geminiToAntigravityUpgrade) Name() string { return "gemini→antigravity backend (v3→v4)" }

// Apply performs the replacement and stamps version 4, a no-op once at
// version 4+. As with earlier steps, stamping the version is itself a valid
// upgrade, so a gemini-free v3 config upgrades simply by gaining `version: 4`.
func (geminiToAntigravityUpgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 4 {
		return false
	}

	// llm.configs entries typed gemini flip to antigravity and shed the
	// gemini-only fields.
	if llm := upgrade.MapValue(root, "llm"); llm != nil && llm.Kind == yaml.MappingNode {
		if configs := upgrade.MapValue(llm, "configs"); configs != nil && configs.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(configs.Content); i += 2 {
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
				upgrade.MapDelete(entry, "trust_workspace")
				upgrade.MapDelete(entry, "approval_mode")
				upgrade.MapDelete(entry, "binary_path")
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
		keyNode, _ := mapEntry(plugins, "gemini")
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

// agentProfileCanonicalizeUpgrade rewrites a per-remote SHORT profile ref stored
// under agents.<name>.profiles ("<remote>/<bundle>#profiles/<name>") to its
// canonical URL form ("<url>@bundles/<bundle>#profiles/<name>"). SetAgent once
// stored these verbatim, so a machine-local alias could persist into config.yaml;
// decision B makes canonical the stored form. This is the one-time
// canonicalize-on-load migration for those already-stored short entries.
//
// It is registry-DEPENDENT (the alias→URL map lives in .ctxloom/remotes.yaml), so
// unlike the version-stamped node upgrades above it is threaded an aliasToURL
// resolver at the load call site (see loadConfigFile) rather than living in the
// static configUpgrades pipeline. It is deliberately NOT version-gated: like the
// directory-profile bundleRefCanonicalizeUpgrade it is naturally idempotent —
// canonical, ctxloom:local, and bare/local refs pass through unchanged, so only
// a resolvable short ref changes — and safe to run every load. When the resolver
// is nil (no registry) it self-gates to a no-op, leaving short refs verbatim for
// the read-path loader to resolve (fault tolerant).
type agentProfileCanonicalizeUpgrade struct {
	aliasToURL func(string) string
}

// Name identifies the upgrade in logs and the rewrite prompt.
func (agentProfileCanonicalizeUpgrade) Name() string { return "canonicalize agent profile refs" }

// Apply canonicalizes each agents.<name>.profiles sequence, reporting whether it
// changed anything. A missing agents map, a non-mapping agent entry, or a missing
// profiles sequence is skipped.
func (u agentProfileCanonicalizeUpgrade) Apply(root *yaml.Node) (changed bool) {
	if u.aliasToURL == nil {
		return false
	}
	agentsNode := upgrade.MapValue(root, "agents")
	if agentsNode == nil || agentsNode.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(agentsNode.Content); i += 2 {
		agent := agentsNode.Content[i+1]
		if agent.Kind != yaml.MappingNode {
			continue
		}
		seq := upgrade.MapValue(agent, "profiles")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range seq.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			if canonical := remote.CanonicalizeProfileShortRef(item.Value, u.aliasToURL); canonical != item.Value {
				item.Value = canonical
				changed = true
			}
		}
	}
	return changed
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

// profileCommandSelectorUpgrade is the v4→v5 config upgrade: inline profile
// definitions that cherry-pick a bundle prompt via a "#prompts/" / ":prompts/"
// item selector are migrated to the "commands" section, matching the
// prompt→skill→command item-kind rename (the one-hop rewrite lands directly on
// "commands" — "skills" is reserved for a different, future item-kind, so a
// permanent rewrite can never target it). It mirrors the directory-profile
// promptSelectorUpgrade (internal/profiles) and the bundle commandsKeyUpgrade
// (internal/bundles) so every load path migrates the legacy vocabulary. A
// comment-preserving yaml.Node rewrite.
type profileCommandSelectorUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (profileCommandSelectorUpgrade) Name() string {
	return "rename profile prompt selectors to commands (v4→v5)"
}

// Apply rewrites prompt selectors in every inline profile's bundles/bundle_items
// and stamps version 5, a no-op at version 5+. As with earlier steps, stamping
// the version is itself a valid upgrade, so a selector-free v4 config upgrades
// simply by gaining `version: 5`.
func (profileCommandSelectorUpgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 5 {
		return false
	}
	if profiles := upgrade.MapValue(root, "profiles"); profiles != nil && profiles.Kind == yaml.MappingNode {
		if defs := upgrade.MapValue(profiles, "definitions"); defs != nil && defs.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(defs.Content); i += 2 {
				prof := defs.Content[i+1]
				if prof.Kind != yaml.MappingNode {
					continue
				}
				rewriteSeqCommandSelectors(prof, "bundles")
				rewriteSeqCommandSelectors(prof, "bundle_items")
			}
		}
	}
	upgrade.SetVersion(root, versionKey, 5)
	return true
}

// rewriteSeqCommandSelectors migrates each scalar entry of the named sequence
// on m that carries a legacy prompt item selector ("#prompts/" / ":prompts/")
// to the commands section, preserving the selector's separator. A
// missing/non-sequence node is a no-op.
func rewriteSeqCommandSelectors(m *yaml.Node, key string) {
	seq := upgrade.MapValue(m, key)
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return
	}
	for _, item := range seq.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		for _, sep := range []string{"#prompts/", ":prompts/"} {
			if strings.Contains(item.Value, sep) {
				item.Value = strings.Replace(item.Value, sep, sep[:1]+"commands/", 1)
				break
			}
		}
	}
}

// defaultAgentUpgrade is the v5→v6 config upgrade: profiles.defaults was RETIRED
// in favor of an always-bound DEFAULT AGENT. A pre-v6 config's default profile
// list is preserved (not dropped — this is lossless) by synthesizing an agent
// named "default" that composes exactly those profiles, pointing default_agent
// at it, and carrying the primary LLM label as the agent's engine so the bare
// `ctxloom run` binds the same engine it used to. It is a comment-preserving
// yaml.Node rewrite (via the shared upgrade helpers), modeled on migrateProfilesV3.
//
// The moves:
//
//   - profiles.defaults (a seq) → agents.default.profiles (the seq node moves so
//     its per-item comments ride along).
//   - llm.defaults.primary value → agents.default.engine (when present), so the
//     synthesized default agent launches the same backend profiles.defaults did.
//   - agents.default.runtime is left unset (empty already resolves to the
//     implicit pre-agent default, "host" — see migrateDefaultAgentV6).
//   - default_agent: default (top level).
//   - delete profiles.defaults; prune an emptied profiles: map; set version 6.
//
// No-clobber: an existing agents.default or a set default_agent is left intact —
// stamping the version alone is a valid upgrade for a config that already carries
// the new shape.
type defaultAgentUpgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (defaultAgentUpgrade) Name() string { return "profiles.defaults → default agent (v5→v6)" }

// Apply performs the reshape and stamps version 6, a no-op once at version 6+.
func (defaultAgentUpgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 6 {
		return false
	}

	migrateDefaultAgentV6(root)

	upgrade.SetVersion(root, versionKey, 6)
	return true
}

// migrateDefaultAgentV6 synthesizes agents.default + default_agent from a legacy
// profiles.defaults seq (see defaultAgentUpgrade). A config with no
// profiles.defaults is left untouched (the version stamp alone upgrades it).
func migrateDefaultAgentV6(root *yaml.Node) {
	prof := upgrade.MapValue(root, "profiles")
	if prof == nil || prof.Kind != yaml.MappingNode {
		return
	}
	defaultsSeq := upgrade.MapValue(prof, "defaults")
	if defaultsSeq == nil {
		return
	}
	// Always drop the retired key, even if we don't synthesize (e.g. it collides
	// with an existing agents.default) — profiles.defaults is no longer valid.
	upgrade.MapDelete(prof, "defaults")
	if len(prof.Content) == 0 {
		upgrade.MapDelete(root, "profiles")
	}

	agentsMap := upgrade.EnsureMap(root, "agents")
	// Don't clobber a hand-authored agents.default; the user's binding wins.
	//
	// But the delete above already ran, so on THIS branch the user's default
	// profile list is gone with nowhere to go — an irreversible on-disk loss
	// (the migration rewrites the file), after which the next run launches with
	// a different profile set than the one they configured. Report it the way
	// migrateLLMv3 reports its own lossy branch: recordMigrationWarning,
	// surfaced as WarnKindMigrationLossy, fatal in strict mode.
	if upgrade.MapValue(agentsMap, "default") == nil {
		entry := upgrade.EnsureMap(agentsMap, "default")
		// engine ← llm.defaults.primary (when present), so the synthesized default
		// agent launches the same backend the retired defaults did.
		if llm := upgrade.MapValue(root, "llm"); llm != nil && llm.Kind == yaml.MappingNode {
			if roleDefaults := upgrade.MapValue(llm, "defaults"); roleDefaults != nil && roleDefaults.Kind == yaml.MappingNode {
				if p := upgrade.MapValue(roleDefaults, "primary"); p != nil && p.Kind == yaml.ScalarNode && p.Value != "" {
					upgrade.MapSet(entry, "engine", upgrade.ScalarNode(p.Value))
				}
			}
		}
		// Move the defaults seq node itself so its item comments survive.
		upgrade.MapSet(entry, "profiles", defaultsSeq)
		// agents.*.runtime is deliberately left UNSET here, not written as an
		// explicit "host": Agent.Runtime's own doc says empty already means
		// "host" (the pre-agent implicit default this migration preserves),
		// so writing it explicitly added no information -- and after
		// layerscope closed the "agents.*.runtime in a committed project
		// file" escalation (ScopeMachine disallows LayerProject), an explicit
		// value synthesized here would be flagged and dropped on the very
		// next load of a migrated PROJECT config, which is worse than never
		// having written it. See config_migrate_test.go for the pinned
		// absence.
	} else {
		recordMigrationWarning("config migration: dropped profiles.defaults %v (agents.default already exists); re-add them under agents.<name>.profiles", scalarSeqValues(defaultsSeq))
	}

	// Point default_agent at the synthesized (or pre-existing) default agent,
	// unless the user already set one.
	if upgrade.MapValue(root, "default_agent") == nil {
		upgrade.MapSet(root, "default_agent", upgrade.ScalarNode("default"))
	}
}

// scalarSeqValues renders a YAML sequence node's scalar items for a
// diagnostic. A lossy-migration message has to NAME what it dropped —
// "dropped your defaults" is not something a user can act on — and the node
// is the only place those values still exist by the time the warning is
// written.
func scalarSeqValues(seq *yaml.Node) []string {
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	out := make([]string, 0, len(seq.Content))
	for _, item := range seq.Content {
		if item.Kind == yaml.ScalarNode {
			out = append(out, item.Value)
		}
	}
	return out
}

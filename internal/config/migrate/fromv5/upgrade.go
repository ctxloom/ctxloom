// Package fromv5 carries every schema migration OFF config version 5, together
// with the tests that prove it.
//
// v5 configs: composition defaults under `profiles.defaults`.
//
// The directory is the unit of support: when v5 configs stop being supported,
// this whole package is deleted and one line leaves config's pipeline. Nothing
// else has to be untangled, which is the entire reason the migrations are keyed
// by SOURCE version rather than by what they migrate toward.
package fromv5

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

// Upgrade is the v5→v6 config upgrade: profiles.defaults was RETIRED
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
type Upgrade struct{ Report upgrade.Reporter }

// Name identifies the upgrade in logs and the rewrite prompt.
func (Upgrade) Name() string { return "profiles.defaults → default agent (v5→v6)" }

// Apply performs the reshape and stamps version 6, a no-op once at version 6+.
func (u Upgrade) Apply(root *yaml.Node) (changed bool) {
	if v, ok := upgrade.Version(root, versionKey); !ok || v >= 6 {
		return false
	}

	migrateDefaultAgentV6(root, u.Report)

	upgrade.SetVersion(root, versionKey, 6)
	return true
}

// migrateDefaultAgentV6 synthesizes agents.default + default_agent from a legacy
// profiles.defaults seq (see Upgrade). A config with no
// profiles.defaults is left untouched (the version stamp alone upgrades it).
func migrateDefaultAgentV6(root *yaml.Node, report upgrade.Reporter) {
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
					upgrade.MapSet(entry, "llm", upgrade.ScalarNode(p.Value))
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
		reportLoss(report, "config migration: dropped profiles.defaults %v (agents.default already exists); re-add them under agents.<name>.profiles", scalarSeqValues(defaultsSeq))
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

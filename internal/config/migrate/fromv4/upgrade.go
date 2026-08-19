// Package fromv4 carries every schema migration OFF config version 4, together
// with the tests that prove it.
//
// v4 configs: profile item selectors still spelled `prompts`.
//
// The directory is the unit of support: when v4 configs stop being supported,
// this whole package is deleted and one line leaves config's pipeline. Nothing
// else has to be untangled, which is the entire reason the migrations are keyed
// by SOURCE version rather than by what they migrate toward.
package fromv4

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
)

// versionKey is the top-level integer schema-version field on config.yaml.
// Declared per package so this directory stays independently deletable.
const versionKey = "version"

// Upgrade is the v4→v5 config upgrade: inline profile
// definitions that cherry-pick a bundle prompt via a "#prompts/" / ":prompts/"
// item selector are migrated to the "commands" section, matching the
// prompt→skill→command item-kind rename (the one-hop rewrite lands directly on
// "commands" — "skills" is reserved for a different, future item-kind, so a
// permanent rewrite can never target it). It mirrors the directory-profile
// promptSelectorUpgrade (internal/profiles) and the bundle commandsKeyUpgrade
// (internal/bundles) so every load path migrates the legacy vocabulary. A
// comment-preserving yaml.Node rewrite.
type Upgrade struct{}

// Name identifies the upgrade in logs and the rewrite prompt.
func (Upgrade) Name() string {
	return "rename profile prompt selectors to commands (v4→v5)"
}

// Apply rewrites prompt selectors in every inline profile's bundles/bundle_items
// and stamps version 5, a no-op at version 5+. As with earlier steps, stamping
// the version is itself a valid upgrade, so a selector-free v4 config upgrades
// simply by gaining `version: 5`.
func (Upgrade) Apply(root *yaml.Node) (changed bool) {
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

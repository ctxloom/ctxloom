package configload

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// profileRefCanonicalizeUpgrade rewrites every agents.<name>.profiles entry
// to its canonical form through canonical. With a context-free canonicalizer
// it is configKind's step (canonicalAgentRefs); with the injected,
// registry-consulting one it is a normalizer (see Sources.normalize) that
// fires only when a ref actually changes.
type profileRefCanonicalizeUpgrade struct {
	name      string
	canonical func(ref string) string
}

func (u profileRefCanonicalizeUpgrade) Name() string { return u.name }

func (u profileRefCanonicalizeUpgrade) Apply(root *yaml.Node) (changed bool) {
	agentsNode := yamlx.MapValue(root, "agents")
	if agentsNode == nil || agentsNode.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(agentsNode.Content); i += 2 {
		agent := agentsNode.Content[i+1]
		if agent.Kind != yaml.MappingNode {
			continue
		}
		seq := yamlx.MapValue(agent, "profiles")
		if seq == nil || seq.Kind != yaml.SequenceNode {
			continue
		}
		for _, item := range seq.Content {
			if item.Kind != yaml.ScalarNode {
				continue
			}
			if canonical := u.canonical(item.Value); canonical != item.Value {
				item.Value = canonical
				changed = true
			}
		}
	}
	return changed
}

// canonicalAgentRefs is the config step that moves every agent binding's
// stored profile refs onto the canonical ctxloom URI grammar
// (remote.CanonicalSpelling). Only the context-free re-spelling is a schema
// step; resolving a short "<alias>/..." ref needs this machine's remotes
// registry and stays a normalizer.
var canonicalAgentRefs upgrade.Upgrader = profileRefCanonicalizeUpgrade{
	name:      "re-spell agent profile refs as canonical ctxloom URIs",
	canonical: remote.CanonicalSpelling,
}

// configKind versions every config.yaml layer. LegacyKey: `version` is an
// older spelling of the same generation number, so a file carrying it reads
// exactly as one carrying schema_version.
var configKind = schemaver.Kind{
	Name:      "ctxloom config",
	LegacyKey: "version",
	Oldest:    6,
	Steps:     []upgrade.Upgrader{canonicalAgentRefs},
}

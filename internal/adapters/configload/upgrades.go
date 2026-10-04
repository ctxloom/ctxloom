package configload

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// profileRefCanonicalizeUpgrade rewrites every agents.<name>.profiles entry
// to its canonical form through the injected canonicalizer. It is a
// normalizer, not a schema step (see Sources.normalize): it fires only when a
// ref actually changes.
type profileRefCanonicalizeUpgrade struct {
	canonical func(ref string) string
}

func (profileRefCanonicalizeUpgrade) Name() string { return "canonicalize agent profile refs" }

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

// configKind versions every config.yaml layer. LegacyKey: config files
// declared their generation as `version` before schemaver existed.
var configKind = schemaver.Kind{
	Name:      "ctxloom config",
	LegacyKey: "version",
	Oldest:    config.CurrentConfigVersion,
}

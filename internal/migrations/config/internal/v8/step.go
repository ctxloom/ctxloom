// Package v8 is the ctxloom config's step to generation 8.
package v8

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Step is generation 7 -> 8: `config.essence_max_chars` goes with session
// compaction. Engines compact their own sessions, so there is no essence for
// a budget to size; the key is dropped rather than refused, because the
// value had no other meaning to carry over.
type Step struct{}

// To is the generation this step migrates to.
func (Step) To() int { return 8 }

// Name describes the step.
func (Step) Name() string { return "drop config.essence_max_chars (session compaction removed)" }

// Apply removes essence_max_chars from root's `config:` section, and the
// section itself when that leaves it empty.
func (Step) Apply(root *yaml.Node) {
	settings := yamlx.MapValue(root, "config")
	if settings == nil || settings.Kind != yaml.MappingNode {
		return
	}
	yamlx.MapDelete(settings, "essence_max_chars")
	if len(settings.Content) == 0 {
		yamlx.MapDelete(root, "config")
	}
}

// Package v3 is the lockfile's step to generation 3.
package v3

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Step is generation 2 -> 3: the lock stops recording when it was written
// (top-level locked_at) and when each bundle was fetched (per-entry
// fetched_at). Both changed on every pull at unchanged pins, so a committed
// lock went dirty without any pin moving; the lock now records only what a
// pull is meant to decide.
type Step struct{}

// To is the generation this step migrates to.
func (Step) To() int { return 3 }

// Name describes the step.
func (Step) Name() string { return "drop locked_at and per-bundle fetched_at" }

// Apply removes both timestamps from root, a lockfile document's mapping.
func (Step) Apply(root *yaml.Node) {
	yamlx.MapDelete(root, "locked_at")
	bundles := yamlx.MapValue(root, "bundles")
	if bundles == nil || bundles.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(bundles.Content); i += 2 {
		if entry := bundles.Content[i]; entry.Kind == yaml.MappingNode {
			yamlx.MapDelete(entry, "fetched_at")
		}
	}
}

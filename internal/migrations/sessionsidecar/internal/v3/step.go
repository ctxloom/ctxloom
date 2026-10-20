// Package v3 is the session sidecar's step to generation 3.
package v3

import (
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Step is generation 2 -> 3: the sidecar stops recording `source_entries`,
// the transcript entry count an essence was compacted from. Session
// compaction is gone, so there is no essence for the count to date.
type Step struct{}

// To is the generation this step migrates to.
func (Step) To() int { return 3 }

// Name describes the step.
func (Step) Name() string { return "drop source_entries (session compaction removed)" }

// Apply removes source_entries from root, a sidecar document's mapping.
func (Step) Apply(root *yaml.Node) {
	yamlx.MapDelete(root, "source_entries")
}

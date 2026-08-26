//go:build acceptance

package acceptance

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestProbeConfigYAML_TaggedProbesCarryTheRuntimeAxis is the acceptance-tagged
// half of the axis-wiring guard. P2's and P3's config builders live in
// //go:build acceptance files, so they cannot be reached from the untagged
// sibling; the assertion is identical and the reason is the same — a probe that
// records an axis but never writes it runs on the HOST while its cell id, tags
// and evidence all say "container".
func TestProbeConfigYAML_TaggedProbesCarryTheRuntimeAxis(t *testing.T) {
	a := liveAgent{config: fmt.Sprintf("version: %d\nllm:\n  configs:\n    claude:\n      type: claude-code\n      model: claude-haiku-4-5-20251001\n", config.CurrentConfigVersion)}

	builders := map[string]func(string) string{
		"p2 mcpProbeConfigYAML":  func(rt string) string { return mcpProbeConfigYAML(a, "claude", rt) },
		"p3 hookProbeConfigYAML": func(rt string) string { return hookProbeConfigYAML(a, "claude", "claude-code", rt) },
	}

	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			for _, rt := range []string{"container-rootless", "container-rootful"} {
				assert.Contains(t, build(rt), "    runtime: "+rt+"\n",
					"%s must write the requested runtime onto the agent binding; without it the cell runs on the HOST and reports itself containerized", name)
			}
			assert.NotContains(t, build("host"), "runtime:",
				"%s must NOT write a runtime line for the host axis", name)
		})
	}
}

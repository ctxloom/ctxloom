package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// TestRenderContainerCheck covers the human rendering across the report's
// shapes: in-container with a reachable runtime and a probe mismatch, and the
// plain-host no-runtime case.
func TestRenderContainerCheck(t *testing.T) {
	t.Run("devcontainer with DooD mismatch", func(t *testing.T) {
		var buf bytes.Buffer
		d := isolation.Diagnosis{
			InContainer: true,
			Markers:     []string{"/.dockerenv", "$DEVCONTAINER"},
			Runtime:     "docker",
			Reachable:   true,
			Image:       "ctxloom-agent-claude:1",
			SharedFS:    "mismatch: marker content mismatch",
			Guidance:    []string{"the daemon does NOT share this process's filesystem"},
		}
		assert.NoError(t, renderContainerCheck(&buf, "claude-code", d))
		out := buf.String()
		assert.Contains(t, out, "in a container:  yes (/.dockerenv, $DEVCONTAINER)")
		assert.Contains(t, out, "runtime:         docker (reachable)")
		assert.Contains(t, out, "agent image:     ctxloom-agent-claude:1 (absent)")
		assert.Contains(t, out, "shared fs:       mismatch: marker content mismatch")
		assert.Contains(t, out, "-> the daemon does NOT share")
	})

	t.Run("plain host without a runtime", func(t *testing.T) {
		var buf bytes.Buffer
		d := isolation.Diagnosis{
			Runtime:  "none",
			SharedFS: "unprobed: no runtime",
			Guidance: []string{"no container runtime is reachable"},
		}
		assert.NoError(t, renderContainerCheck(&buf, "kiro", d))
		out := buf.String()
		assert.Contains(t, out, "in a container:  no")
		assert.Contains(t, out, "runtime:         none")
		assert.Contains(t, out, "shared fs:       unprobed: no runtime")
	})
}

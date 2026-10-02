package managedhooks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestHookExecPayload_CoversExecFormArgs: a profile hook's trust preimage is
// what runs. An exec-form hook's arguments are part of it — changing one
// cannot ride an earlier grant — and it binds to the same bytes as the shell
// hook whose line runs the same argv. A shell-form hook's preimage is the
// bundle primitive's, unchanged, so every grant already made still holds.
func TestHookExecPayload_CoversExecFormArgs(t *testing.T) {
	exec := wire.Hook{Type: "command", Command: "ctxloom", Args: []string{"hook", "next-step"}}
	other := wire.Hook{Type: "command", Command: "ctxloom", Args: []string{"hook", "mail-drain"}}
	assert.NotEqual(t, hookExecPayload(exec), hookExecPayload(other), "the arguments are part of what runs")
	assert.Equal(t, hookExecPayload(wire.Hook{Type: "command", Command: exec.Line()}), hookExecPayload(exec))

	shell := wire.Hook{Type: "command", Command: "./scripts/check.sh", Matcher: "Bash"}
	want, err := (&bundles.BundleHook{Type: "command", Command: "./scripts/check.sh", Matcher: "Bash"}).ContentPayload()
	require.NoError(t, err)
	assert.Equal(t, want, hookExecPayload(shell))
}

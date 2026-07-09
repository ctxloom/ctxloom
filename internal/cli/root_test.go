package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Execute owns error printing. Without the silence flags cobra prints every
// RunE error twice and dumps the full usage text — including for a wrapped
// LLM's ordinary nonzero exit (run returns ExitError so deferred cleanup can
// run before the process exits).
func TestRootCommand_SilencesCobraErrorNoise(t *testing.T) {
	assert.True(t, rootCmd.SilenceUsage, "usage spam on every error")
	assert.True(t, rootCmd.SilenceErrors, "errors would print twice")
}

// `ctxloom mcp <anything>` must reject unknown subcommands: cobra's legacy
// arg handling would otherwise run the parent RunE — silently starting a
// stdio MCP server that sits waiting on stdin.
func TestMCPCommand_RejectsUnknownSubcommands(t *testing.T) {
	require.NotNil(t, mcpCmd.Args)
	assert.Error(t, mcpCmd.Args(mcpCmd, []string{"list"}))
	assert.NoError(t, mcpCmd.Args(mcpCmd, nil))
}

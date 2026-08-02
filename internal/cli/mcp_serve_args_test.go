package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMcpServeCmd_RejectsStrayArgs pins the fix that `ctxloom mcp` carries
// Args: cobra.NoArgs with a comment saying that without it a stale invocation
// like `ctxloom mcp list` "would silently start a stdio MCP server that sits
// waiting on stdin" — but `ctxloom mcp serve`, which shares the very same
// runMCPServerSDK RunE, declared no Args at all, so cobra's ArbitraryArgs
// default let `ctxloom mcp serve list` do exactly that. Both entries to the
// stdio server must refuse stray args.
func TestMcpServeCmd_RejectsStrayArgs(t *testing.T) {
	for _, cmd := range []*cobra.Command{mcpCmd, mcpServeCmd} {
		require.NotNil(t, cmd.Args, "%s must declare an Args validator", cmd.Name())
		assert.Error(t, cmd.Args(cmd, []string{"list"}),
			"%s must reject a stray arg rather than silently serving on stdin", cmd.Name())
		assert.NoError(t, cmd.Args(cmd, nil),
			"%s must still accept its bare invocation", cmd.Name())
	}
}

package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMcpServerShow_NotFound_TextAndJSONAgree pins the fix that `mcp server
// show <missing>` used to error on the text path but exit 0 with `--format
// json` (the not-found check lived INSIDE emit()'s text closure, which
// cliemit.Emit only runs for FormatText — every structured format fell
// through to clifmt.Render(result, format), rendering {"found":false,...}
// as a success). Both formats must now refuse identically: a
// machine caller asking for JSON must not be told a missing server is a
// clean, present result.
func TestMcpServerShow_NotFound_TextAndJSONAgree(t *testing.T) {
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			testsupport.ProjectDir(t)
			resetApp()
			t.Cleanup(resetApp)

			var out bytes.Buffer
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&out)
			rootCmd.SetArgs([]string{"mcp", "server", "show", "no-such-server", "--format", format})
			t.Cleanup(func() {
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
				rootCmd.SetArgs(nil)
			})

			err := rootCmd.Execute()
			require.Error(t, err, "%s: a missing MCP server must be an error, not a clean {\"found\":false} success", format)
			require.Contains(t, err.Error(), "not found")
		})
	}
}

// TestPrintMCPServerEntry_SessionEndpointEntry_DescribesItWithoutACommand
// pins how ctxloom's own entry reads at rest: `mcp server show ctxloom`
// describes it as served by the running session's endpoint and prints NO
// command line — there is nothing executable to show, because the session
// injects the endpoint and nothing else ever launches ctxloom as a server.
func TestPrintMCPServerEntry_SessionEndpointEntry_DescribesItWithoutACommand(t *testing.T) {
	var out bytes.Buffer
	printMCPServerEntry(&out, operations.MCPServerEntry{Name: "ctxloom", Source: "ctxloom+companion:ctxloom", ServedBy: wire.ServedBySessionEndpoint})
	require.Contains(t, out.String(), "Served by: the running session's endpoint")
	require.NotContains(t, out.String(), "Command:", "nothing executable is shown for a dynamic entry")

	out.Reset()
	printMCPServerEntry(&out, operations.MCPServerEntry{Name: "tasks", Source: "demo", Command: "taskloom", Args: []string{"mcp"}})
	require.Contains(t, out.String(), "Command: taskloom")
	require.NotContains(t, out.String(), "Served by")
}

package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestMcpServerShow_NotFound_TextAndJSONAgree pins U037-F17: `mcp server
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
			config.Invalidate()
			t.Cleanup(config.Invalidate)

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

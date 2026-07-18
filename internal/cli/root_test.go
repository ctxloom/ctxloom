package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt"
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

// reportExecuteError is Execute()'s error-printing tail, extracted so it's
// testable without the process-ending os.Exit calls around it. --format
// text keeps the exact "Error: msg\n" line Execute() always printed (a
// script scraping that line today sees no change); the other four formats
// now get clifmt's structured {"error": "..."} envelope instead of the same
// human line regardless of --format, which was the one place in the CLI
// where --format=json still leaked plain text onto stderr.
func TestReportExecuteError_Text_MatchesPreClifmtLine(t *testing.T) {
	var buf bytes.Buffer
	reportExecuteError(&buf, errors.New("boom"), clifmt.FormatText)
	assert.Equal(t, "Error: boom\n", buf.String())
}

func TestReportExecuteError_JSON_EmitsStructuredEnvelope(t *testing.T) {
	var buf bytes.Buffer
	reportExecuteError(&buf, errors.New("boom"), clifmt.FormatJSON)
	assert.JSONEq(t, `{"error":"boom"}`, buf.String())
}

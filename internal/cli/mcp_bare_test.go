package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// presentTerminal points the isInteractiveTerminal seam at a fixed answer for
// the duration of one test, so both halves of the human/machine split are
// drivable. A test binary's stdin and stdout are never terminals, so without
// this seam only the machine half could ever be exercised — and the human half
// is where the bare noun's whole answer lives.
func presentTerminal(t *testing.T, interactive bool) {
	t.Helper()
	saved := isInteractiveTerminal
	t.Cleanup(func() { isInteractiveTerminal = saved })
	isInteractiveTerminal = func() bool { return interactive }
}

// TestMcpBare_AnswersAHumanWithTheServerListing pins that `ctxloom mcp` typed
// at a terminal is the bare-noun ladder's answer for this noun: the configured
// MCP servers, byte-for-byte what the explicit spelling prints.
//
// Byte equality is the assertion that bites. "Does not print help" passes
// against a command that prints nothing at all, which is this project's
// characteristic silent no-op.
func TestMcpBare_AnswersAHumanWithTheServerListing(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, true)

	bare, err := runRoot(t, "mcp")
	require.NoError(t, err, "bare `ctxloom mcp` answers a human")
	listed, err := runRoot(t, "mcp", "server", "list")
	require.NoError(t, err)

	assert.Equal(t, listed, bare,
		"bare `ctxloom mcp` is the same entry point as `ctxloom mcp server list`")
	assert.NotContains(t, bare, usageMarker,
		"bare `ctxloom mcp` answers with the listing; help has its own spelling")
	assert.NotEmpty(t, strings.TrimSpace(bare),
		"an empty answer is the silent no-op, not a listing")
}

// TestMcpBare_RefusesAMachineAndNamesServe is the loud half of the break.
//
// A protocol client whose configured invocation is the bare noun opens a pipe
// and waits for JSON-RPC. A server listing written into that pipe is not
// merely wrong — it is indistinguishable from a hang: the client sees bytes it
// cannot frame, no initialize response, and nothing anywhere naming the cause.
// Off a terminal the bare noun therefore refuses outright and names the
// spelling that IS the server.
func TestMcpBare_RefusesAMachineAndNamesServe(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, false)

	out, err := runRoot(t, "mcp")

	require.Error(t, err, "off a terminal the bare noun must refuse, not answer")
	assert.Contains(t, err.Error(), "ctxloom mcp serve",
		"the refusal names the invocation that is the stdio server")
	assert.NotContains(t, out, "Auto-register",
		"no part of the server listing may reach a caller framing JSON-RPC")
}

// TestMcpBare_MachineRefusalSurvivesAFormatRequest covers the shape a script
// reaches for next. `--format json` does not make a listing safe to deliver to
// a client that asked for a protocol stream, so the refusal is unconditional
// on the encoding; a script that wants the servers as data asks the leaf that
// produces them.
func TestMcpBare_MachineRefusalSurvivesAFormatRequest(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, false)

	_, err := runRoot(t, "--format", "json", "mcp")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ctxloom mcp server list",
		"the refusal names the leaf a script should call for the listing as data")
}

// TestMcpBare_RejectsAStrayArgAsAnUnknownSubcommand pins that the group node's
// guard is what refuses `ctxloom mcp list`. A namespace that printed help and
// exited 0 for a mistyped verb is the dispatch-level silent no-op groupNode
// exists to close.
func TestMcpBare_RejectsAStrayArgAsAnUnknownSubcommand(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, true)

	_, err := runRoot(t, "mcp", "list")

	require.Error(t, err, "a stray verb must fail rather than serve or teach")
	assert.Contains(t, err.Error(), "unknown command")
}

// TestMcpServe_IsTheOnlyStdioServerEntryPoint pins the machine surface: one
// spelling, symmetric with `acp serve`, and it still refuses stray args rather
// than sitting on stdin.
func TestMcpServe_IsTheOnlyStdioServerEntryPoint(t *testing.T) {
	require.NotNil(t, mcpServeCmd.Args, "mcp serve must declare an Args validator")
	assert.Error(t, mcpServeCmd.Args(mcpServeCmd, []string{"list"}),
		"a stray arg must be refused, not served on stdin")
	assert.NoError(t, mcpServeCmd.Args(mcpServeCmd, nil))

	assert.True(t, isGroupNode(mcpCmd),
		"the `mcp` noun is a namespace; the server is its `serve` leaf")

	child, ok := groupNodeDefaultChild(mcpCmd)
	require.True(t, ok, "bare `mcp` answers with a default view")
	assert.Equal(t, "server", child,
		"the bare noun's answer is the configured-server listing")
}

// TestCtxloomMCPArgs_NamesTheServeLeaf pins the argv every materialized engine
// surface carries. This one value is what .mcp.json, .agents/mcp_config.json,
// .kiro/settings/mcp.json, .codex/config.toml and opencode.json all emit, so a
// drift here is a drift in every engine at once.
func TestCtxloomMCPArgs_NamesTheServeLeaf(t *testing.T) {
	assert.Equal(t, []string{"mcp", "serve"}, agent.CtxloomMCPArgs,
		"a materialized entry must invoke the stdio server, not the listing")

	serve := findSub(findSub(rootCmd, "mcp"), "serve")
	require.NotNil(t, serve)
	assert.Equal(t, agent.CtxloomMCPArgs, mcpArgvPath(serve),
		"the emitted argv is the real command path, not a hand-kept twin")
}

// mcpArgvPath is the command path of cmd below the root, as argv tokens.
func mcpArgvPath(cmd *cobra.Command) []string {
	return strings.Fields(strings.TrimPrefix(cmd.CommandPath(), "ctxloom "))
}

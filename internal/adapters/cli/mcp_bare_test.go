package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// TestMcpBare_RefusesAMachineAndSaysThereIsNoServer is the loud half of the
// break.
//
// A protocol client whose configured invocation is the bare noun opens a pipe
// and waits for JSON-RPC. A server listing written into that pipe is not
// merely wrong — it is indistinguishable from a hang: the client sees bytes it
// cannot frame, no initialize response, and nothing anywhere naming the cause.
// Off a terminal the bare noun therefore refuses outright and says what IS
// the server: the running session's endpoint, not any ctxloom command.
func TestMcpBare_RefusesAMachineAndSaysThereIsNoServer(t *testing.T) {
	remoteBareFixture(t)
	presentTerminal(t, false)

	out, err := runRoot(t, "mcp")

	require.Error(t, err, "off a terminal the bare noun must refuse, not answer")
	assert.Contains(t, err.Error(), "no stdio MCP server",
		"the refusal says no ctxloom command speaks the protocol")
	assert.Contains(t, err.Error(), "session's endpoint",
		"the refusal names what serves ctxloom's tools")
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

// TestMcpNoun_HasNoServeLeaf pins that the noun is a namespace whose default
// view is the configured-server listing, and that NO `serve` leaf exists:
// ctxloom's server is the running session's endpoint, and a command that
// answered the protocol here would be a second, unauthenticated door to it.
func TestMcpNoun_HasNoServeLeaf(t *testing.T) {
	assert.True(t, isGroupNode(mcpCmd),
		"the `mcp` noun is a namespace")

	child, ok := groupNodeDefaultChild(mcpCmd)
	require.True(t, ok, "bare `mcp` answers with a default view")
	assert.Equal(t, "server", child,
		"the bare noun's answer is the configured-server listing")

	assert.Nil(t, findSub(mcpCmd, "serve"), "no ctxloom command speaks the MCP protocol")
}

// TestRunMCPServerEdit_RefusesAnythingButABundleScopedMCPRef pins the refusal
// `mcp server edit` is built around: the config-level (config.yaml
// `mcp.servers`) store has no editor path, so a ref that does not select a
// bundle-scoped MCP server must be refused BY NAME. Editing the wrong store —
// or reporting success having changed nothing — is the failure mode this
// exists to prevent, and it is reached by a selector naming another kind just
// as easily as by one with no selector at all.
func TestRunMCPServerEdit_RefusesAnythingButABundleScopedMCPRef(t *testing.T) {
	c, _ := testCmd()
	for _, ref := range []string{
		"postgres",             // config-level server, no bundle scope
		"demo#fragments/tdd",   // a selector, but for another kind
		"demo#commands/review", // likewise
		"demo#widgets/x",       // unrecognized kind word
	} {
		err := runMCPServerEdit(c, []string{ref})
		require.Error(t, err, ref)
		assert.Contains(t, err.Error(), "not a bundle-scoped ref", ref)
	}

	err := runMCPServerEdit(c, []string{"#mcp/pg"})
	require.Error(t, err, "a selector with no bundle before it addresses nothing")
	assert.Contains(t, err.Error(), "incomplete ref")
}

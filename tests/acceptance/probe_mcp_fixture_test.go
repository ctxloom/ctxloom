// Untagged, like the fixture it checks: `just test` runs all of this without a
// built binary, a live engine or a paid turn.
//
// P2's cells are all @live, so these tests are the ONLY thing that ever executes
// the fixture server and the verdict outside a paid run. Two jobs:
//
//  1. DRIVE THE SERVER FOR REAL. The script is spoken to over stdio exactly as an
//     MCP client speaks to it — initialize, initialized, tools/list, tools/call —
//     and its replies are parsed. A fixture server that could not complete a
//     handshake would make every live cell red with an MCP-delivery failure that
//     is really a bug in this file, and the cost of finding that out is a paid
//     turn per engine.
//
//  2. REFUSE EVERY FALSE GREEN. The verdict's whole value is that it cannot be
//     satisfied without the round trip. The tests below attempt each way a cell
//     could look green without one — most importantly the FOREIGN-CHANNEL attempt
//     (§5.1 of the design): the harp arriving through composed context instead of
//     the tool result, with the server never called. That must red, and it must
//     red as an MCP-DELIVERY failure. If it ever passes, P2 is a tautology and the
//     probe is worthless.
package acceptance

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- driving the real server ---------------------------------------------------

// mcpTestClient is the smallest MCP stdio client that can prove a round trip:
// line-delimited JSON-RPC over the server's stdin/stdout.
type mcpTestClient struct {
	t   *testing.T
	cmd *exec.Cmd
	in  io.WriteCloser
	out *bufio.Reader
}

func startFixtureServer(t *testing.T, nonce string) (*mcpTestClient, probeMCPFixture) {
	t.Helper()
	f, err := probeMCPWriteFixture(t.TempDir(), nonce)
	require.NoError(t, err)

	cmd := exec.Command(f.Binary, f.Dir)
	in, err := cmd.StdinPipe()
	require.NoError(t, err)
	out, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = in.Close()
		_ = cmd.Wait()
	})
	return &mcpTestClient{t: t, cmd: cmd, in: in, out: bufio.NewReader(out)}, f
}

// call sends a request and reads the reply. A notification (id nil) is sent and
// NOT read from, which is the property the server has to honour.
func (c *mcpTestClient) call(id any, method string, params map[string]any) map[string]any {
	c.t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		req["id"] = id
	}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	require.NoError(c.t, err)
	_, err = c.in.Write(append(b, '\n'))
	require.NoError(c.t, err)
	if id == nil {
		return nil
	}
	line, err := c.out.ReadString('\n')
	require.NoError(c.t, err, "the fixture MCP server did not answer %s — a live cell would report this as an MCP-delivery failure and the fault would be here", method)
	var resp map[string]any
	require.NoError(c.t, json.Unmarshal([]byte(line), &resp), "server reply is not JSON: %s", line)
	return resp
}

// TestProbeMCPFixture_ServesTheRoundTripOverStdio is the hermetic half of P2:
// the same conversation a real engine has with this server, driven here for free.
func TestProbeMCPFixture_ServesTheRoundTripOverStdio(t *testing.T) {
	const nonce = "swift-amber-falcon"
	c, f := startFixtureServer(t, nonce)

	t.Run("initialize echoes the client's protocol version and declares tools", func(t *testing.T) {
		resp := c.call(1, "initialize", map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "ctxloom-probe-test", "version": "0"},
		})
		res, ok := resp["result"].(map[string]any)
		require.True(t, ok, "initialize must succeed, got %v", resp)
		require.Equal(t, "2025-06-18", res["protocolVersion"],
			"the server must echo the client's protocol version; four vendor clients negotiate differently and a fixed version reds cells for the fixture's reason")
		caps, ok := res["capabilities"].(map[string]any)
		require.True(t, ok)
		require.Contains(t, caps, "tools", "a server that declares no tools capability is never asked for its tools")
	})

	// The initialized NOTIFICATION carries no id. The server must not answer it;
	// if it does, the next read below picks up the stray reply and the test fails
	// on a mismatched id — which is exactly how a real client breaks.
	c.call(nil, "notifications/initialized", nil)

	t.Run("tools/list advertises get_nonce", func(t *testing.T) {
		resp := c.call(2, "tools/list", nil)
		require.EqualValues(t, 2, resp["id"], "the server answered a notification, so replies are now off by one")
		res, ok := resp["result"].(map[string]any)
		require.True(t, ok, "tools/list must succeed, got %v", resp)
		tools, ok := res["tools"].([]any)
		require.True(t, ok)
		require.Len(t, tools, 1)
		tool := tools[0].(map[string]any)
		require.Equal(t, probeMCPToolName, tool["name"])
		require.NotEmpty(t, tool["description"], "an undescribed tool is one a model has no reason to call")
		require.Contains(t, tool, "inputSchema", "a tool with no input schema is rejected by some clients")
	})

	t.Run("tools/call returns the nonce and records the call", func(t *testing.T) {
		resp := c.call(3, "tools/call", map[string]any{"name": probeMCPToolName, "arguments": map[string]any{}})
		res, ok := resp["result"].(map[string]any)
		require.True(t, ok, "tools/call must succeed, got %v", resp)
		content, ok := res["content"].([]any)
		require.True(t, ok)
		require.Len(t, content, 1)
		item := content[0].(map[string]any)
		require.Equal(t, "text", item["type"])
		require.Equal(t, nonce, item["text"], "the tool result IS the channel; a result that does not carry the minted harp makes every live cell red for the fixture's reason")

		calls, existed, err := probeMCPCallLog(f.CallLog)
		require.NoError(t, err)
		require.True(t, existed, "the server must write its call log — the verdict's tool-path rung reads nothing otherwise and P2 collapses into a string match")
		require.Equal(t, 1, probeMCPToolCallCount(calls))
	})

	t.Run("a namespaced tool name still resolves", func(t *testing.T) {
		// claude presents this tool as mcp__probenonce__get_nonce. A client that
		// echoes its own namespaced name back in tools/call must still be served,
		// or the cell reds on a naming convention rather than on the capability.
		resp := c.call(4, "tools/call", map[string]any{
			"name": "mcp__" + probeMCPServerName + "__" + probeMCPToolName,
		})
		res, ok := resp["result"].(map[string]any)
		require.True(t, ok, "a namespaced tool name must resolve, got %v", resp)
		require.Equal(t, nonce, res["content"].([]any)[0].(map[string]any)["text"])
	})

	t.Run("an unknown tool is refused, not answered with the nonce", func(t *testing.T) {
		resp := c.call(5, "tools/call", map[string]any{"name": "something_else"})
		require.Contains(t, resp, "error", "a server that hands the nonce to any tool name is not testing a tool")
		require.NotContains(t, mustJSON(t, resp), nonce)

		calls, _, err := probeMCPCallLog(f.CallLog)
		require.NoError(t, err)
		require.Equal(t, 2, probeMCPToolCallCount(calls),
			"an unknown tool must NOT count as a get_nonce call — the verdict's rung would then be satisfied by any tool call at all")
	})

	t.Run("an unknown method gets a JSON-RPC error, not silence", func(t *testing.T) {
		resp := c.call(6, "completely/unknown", nil)
		errObj, ok := resp["error"].(map[string]any)
		require.True(t, ok, "a silent drop hangs the client, and a hung client reads as an engine that never called the tool")
		require.EqualValues(t, -32601, errObj["code"])
	})

	t.Run("the call log never carries the nonce", func(t *testing.T) {
		// The log is evidence, and evidence must not become a second channel to
		// the value: it is written to a path the engine could in principle read.
		body, err := probeFileArtifact("the fixture MCP server's call log", f.CallLog)
		require.NoError(t, err)
		require.NotContains(t, body.Body, nonce,
			"the call log holds the nonce — it is now a channel to the value that this probe does not test, and an engine that read it would false-green the cell")
	})
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// TestProbeMCPFixture_ScriptDoesNotLeakIntoTheWorkspace pins the two properties
// the written fixture must have before a cell is worth running.
func TestProbeMCPFixture_ScriptDoesNotLeakIntoTheWorkspace(t *testing.T) {
	t.Run("an empty nonce is refused", func(t *testing.T) {
		_, err := probeMCPWriteFixture(t.TempDir(), "")
		require.Error(t, err, "a server serving the empty string satisfies strings.Contains against every output there is")
		require.Contains(t, err.Error(), "empty nonce")
	})

	t.Run("the nonce lives beside the binary and NEVER in the registration", func(t *testing.T) {
		const nonce = "brisk-cobalt-heron"
		f, err := probeMCPWriteFixture(t.TempDir(), nonce)
		require.NoError(t, err)

		got, err := probeFileArtifact("nonce", f.NoncePath)
		require.NoError(t, err)
		require.Contains(t, got.Body, nonce)

		// THE INVARIANT THAT REPLACED "the script carries the nonce". The
		// server is now a binary taking the fixture DIRECTORY as its only
		// argument, and config.yaml — which lives inside the workspace and is
		// readable by the agent under test — must not carry the harp. A nonce
		// on argv would be reachable without a tool call, and every cell would
		// pass while proving nothing.
		reg := probeMCPConfigYAML(f.Binary, f.Dir)
		require.NotContains(t, reg, nonce,
			"the registration names the fixture directory, never the harp: a nonce in config.yaml is readable from the agent's own workspace, so the round-trip assertion would pass without a round trip")
	})

	t.Run("the call log is absent until the server runs", func(t *testing.T) {
		f, err := probeMCPWriteFixture(t.TempDir(), "quiet-umber-lynx")
		require.NoError(t, err)
		calls, existed, err := probeMCPCallLog(f.CallLog)
		require.NoError(t, err)
		require.False(t, existed, "pre-creating the log would erase the difference between 'the server never started' and 'the tool was never called' — two findings about two different subsystems")
		require.Empty(t, calls)
	})
}

// TestProbeMCPFixture_ConfigRegistrationIsTheProductionSurface pins what the
// fixture writes into config.yaml. It must be the plain `mcp.servers` grammar
// wire.MCPConfig parses — nothing engine-specific, because the whole claim is
// that ctxloom carried it to each engine's own surface.
func TestProbeMCPFixture_ConfigRegistrationIsTheProductionSurface(t *testing.T) {
	got := probeMCPConfigYAML("/opt/fix ture/nonce-mcp-server", "/tmp/x y/fixture")
	require.Contains(t, got, "mcp:\n  servers:\n    "+probeMCPServerName+":\n")
	require.Contains(t, got, `command: "/opt/fix ture/nonce-mcp-server"`)
	require.Contains(t, got, `- "/tmp/x y/fixture"`,
		"paths are quoted: a temp directory with a space in it would otherwise produce a YAML list item that parses as something else entirely")
	require.NotContains(t, got, "mcpServers",
		"the fixture must write ctxloom's config grammar, not an engine's native file — writing the engine file directly would bypass the delivery this probe exists to test")
}

// --- the verdict's refusals ------------------------------------------------------

func mcpTestCell() probeCellID {
	return probeCellID{Probe: probeP2, Engine: "claude-code", Runtime: "host", Workspace: "none"}
}

func toolCalled(n int) []probeMCPCall {
	calls := []probeMCPCall{{Event: "start"}, {Event: "request", Detail: map[string]any{"method": "initialize"}}}
	for i := 0; i < n; i++ {
		calls = append(calls, probeMCPCall{Event: "tool_call", Detail: map[string]any{"name": probeMCPToolName}})
	}
	return calls
}

func TestMCPProbeAssert_AcceptsOnlyTheRoundTrip(t *testing.T) {
	const nonce = "swift-amber-falcon"

	t.Run("the honest pass", func(t *testing.T) {
		require.NoError(t, mcpProbeAssert(mcpProbeRun{
			Cell:     mcpTestCell(),
			Nonce:    nonce,
			Run:      probeRun{Stdout: `{"nonce":"swift-amber-falcon"}`},
			CallLog:  toolCalled(1),
			LogFound: true,
		}))
	})

	t.Run("surrounding whitespace and a trailing newline are tolerated", func(t *testing.T) {
		require.NoError(t, mcpProbeAssert(mcpProbeRun{
			Cell:     mcpTestCell(),
			Nonce:    nonce,
			Run:      probeRun{Stdout: "\n  {\"nonce\":\"swift-amber-falcon\"}  \n"},
			CallLog:  toolCalled(1),
			LogFound: true,
		}))
	})

	// THE TAUTOLOGY CHECK, run as a test rather than trusted as an argument.
	// The nonce arrives through composed context (the foreign channel P0 owns)
	// and the tool is never called. The output is byte-identical to the honest
	// pass above. It MUST red, and it must red on the tool path.
	t.Run("foreign channel: the right answer with no tool call is an MCP-DELIVERY failure", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell:  mcpTestCell(),
			Nonce: nonce,
			Run:   probeRun{Stdout: `{"nonce":"swift-amber-falcon"}`},
			// The server started (handshake logged) and get_nonce was never
			// invoked: the model produced the value from somewhere else.
			CallLog:  []probeMCPCall{{Event: "start"}, {Event: "request", Detail: map[string]any{"method": "initialize"}}},
			LogFound: true,
		})
		require.Error(t, err, "P2 is a TAUTOLOGY if a correct-looking answer passes without the tool ever being called — the probe would be measuring string equality, not a round trip")
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, channelMCPToolResult.Shape, shape)
		require.Contains(t, err.Error(), "NEVER CALLED")
	})

	t.Run("no log file at all names the wiring, not the model", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell:     mcpTestCell(),
			Nonce:    nonce,
			Run:      probeRun{Stdout: `{"nonce":"swift-amber-falcon"}`},
			LogFound: false,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "NEVER STARTED",
			"'the server never started' and 'the tool was never called' blame different subsystems and must never be merged")
	})

	t.Run("a run that failed outright is a RUN failure, not an MCP one", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run: probeRun{Stdout: "", Stderr: "boom", ExitCode: 1, Err: errFake("exit 1")},
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, shapeRunFailed, shape, "an MCP delivery failure must never absorb an engine that did not start")
	})

	t.Run("exit 0 with empty stdout is the silent no-op, named", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run: probeRun{Stdout: "   \n"}, CallLog: toolCalled(1), LogFound: true,
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, shapeSilentNoOp, shape)
	})

	t.Run("fenced output is a FORMAT failure once the round trip is done", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run:     probeRun{Stdout: "```json\n{\"nonce\":\"swift-amber-falcon\"}\n```"},
			CallLog: toolCalled(1), LogFound: true,
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, shapeOutputFormat, shape,
			"putting the tool-path rung first must not swallow a genuine output-contract finding: the tool WAS called here, so the form check gets to speak. Loosening this to strip fences would delete the signal the whole ladder shares.")
	})

	// The kiro run, in miniature. Under the floor's check order
	// this reported OUTPUT-FORMAT — terminal decoration — while the model's own
	// answer said the tool was not found, and the shape a sweep diffs named the
	// wrong subsystem entirely.
	t.Run("malformed output with no tool call is an MCP finding, not a format one", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run:      probeRun{Stdout: "\x1b[38;5;141m> \x1b[0m{\"nonce\":\"tool not found\"}\x1b[0m"},
			CallLog:  []probeMCPCall{{Event: "start"}},
			LogFound: true,
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, channelMCPToolResult.Shape, shape,
			"a probe about MCP that reds on ANSI colour codes and never mentions MCP is attributing to the wrong subsystem — the tool-call fact is independent of the output's shape and is this probe's subject")
		require.Contains(t, err.Error(), "NEVER CALLED")
		require.Contains(t, err.Error(), "tool not found",
			"the raw stdout must ride the message: the most interesting version of this red is what the model said about why")
	})

	t.Run("the tool was called and the model reported something else: VALUE failure", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run:     probeRun{Stdout: `{"nonce":"i-called-the-tool-honest"}`},
			CallLog: toolCalled(1), LogFound: true,
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, channelMCPToolResult.Shape, shape,
			"a well-formed object with the wrong value is still the nonce not arriving — carriesNonce speaks first, and it speaks in the channel's own voice")
	})

	t.Run("extra keys are a SHAPE failure", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run:     probeRun{Stdout: `{"nonce":"swift-amber-falcon","note":"called the tool"}`},
			CallLog: toolCalled(1), LogFound: true,
		})
		shape, ok := probeShapeOf(err)
		require.True(t, ok)
		require.Equal(t, shapeShape, shape)
	})

	t.Run("an empty minted nonce cannot pass vacuously", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: "",
			Run:     probeRun{Stdout: `{"nonce":"anything at all"}`},
			CallLog: toolCalled(1), LogFound: true,
		})
		require.Error(t, err, "every string contains the empty string; a cell that forgot to mint must not sail through the one check that proves its channel")
	})

	t.Run("the failure names the cell it is about", func(t *testing.T) {
		err := mcpProbeAssert(mcpProbeRun{
			Cell: mcpTestCell(), Nonce: nonce,
			Run: probeRun{Stdout: `{"nonce":"swift-amber-falcon"}`}, LogFound: false,
		})
		require.Contains(t, err.Error(), "probe="+probeP2)
		require.Contains(t, err.Error(), "engine=claude-code")
	})
}

// TestProbeMCPCallLog_RefusesToReadNothingAsNoCall keeps the evidence reader
// honest: a malformed log must be an error, never an empty call list that the
// verdict would then report as "the tool was never called".
func TestProbeMCPCallLog_RefusesToReadNothingAsNoCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, probeMCPLogName)
	require.NoError(t, os.WriteFile(path, []byte("{\"event\":\"tool_call\"}\nnot json at all\n"), 0o600))
	_, existed, err := probeMCPCallLog(path)
	require.True(t, existed)
	require.Error(t, err, "an unparseable record is a FIXTURE fault; swallowing it would be reported as a model that never called the tool")
	require.Contains(t, err.Error(), "unparseable record")
}

// TestProbeMCPToolCallCount_CountsOnlyRealInvocations guards the one counter the
// tool-path rung reads.
func TestProbeMCPToolCallCount_CountsOnlyRealInvocations(t *testing.T) {
	calls := []probeMCPCall{
		{Event: "start"},
		{Event: "request", Detail: map[string]any{"method": "tools/call"}},
		{Event: "tool_call_unknown", Detail: map[string]any{"name": "other"}},
		{Event: "tool_call", Detail: map[string]any{"name": probeMCPToolName}},
		{Event: "eof"},
	}
	require.Equal(t, 1, probeMCPToolCallCount(calls),
		"only a served get_nonce counts: a request record or a refused unknown tool would let the rung be satisfied by a handshake")
}

// TestProbeMCPSummary_ShowsHowFarTheHandshakeGot: the summary is what a human
// reads first on a red cell.
func TestProbeMCPSummary_ShowsHowFarTheHandshakeGot(t *testing.T) {
	require.Equal(t, "(none)", probeMCPCallLogSummary(nil))
	got := probeMCPCallLogSummary([]probeMCPCall{
		{Event: "start"},
		{Event: "request", Detail: map[string]any{"method": "initialize"}},
		{Event: "request", Detail: map[string]any{"method": "tools/list"}},
	})
	require.Equal(t, "start request(initialize) request(tools/list)", got)
}

// errFake is a run-level error stand-in: the verdict only ever reads that the
// run errored, never what the error was.
type errFake string

func (e errFake) Error() string { return string(e) }

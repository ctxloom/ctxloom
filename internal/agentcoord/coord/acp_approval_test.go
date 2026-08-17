package coord

import (
	"encoding/json"

	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// LIVE-PATH REPRODUCTION. Every other approval test in this
// package scripts the engine (scriptedChat) — a Go double that hands the
// EngineHost a ChatEvent.Permission directly and reads its answer straight
// off `in`. That proves the coordinator's half of the ladder but skips the
// ONE thing a live delegated child actually does: speak ACP over a pipe to a
// separate process, whose session/request_permission has to survive
// internal/acp's session loop, the EngineHost's forward, the RunChannel, the
// coordinator's ladder, and the whole way back.
//
// This file closes that gap without a vendor binary or a paid token: the
// engine is the REAL internal/acp driver, and the agent it drives is a real
// subprocess (this test binary, re-executed as fakeACPAgent below) speaking
// real newline-delimited JSON-RPC over real pipes. Everything between the
// two — EngineHost, Home, gRPC RunChannel, Coordinator, ladder, journals —
// is production code.
//
// A hang on this path is exactly what this reproduces when the path
// is broken: the child's turn never completes, so its roster state never
// leaves Executing and the marker never lands.

// fakeACPAgentEnv marks a spawned copy of this test binary as the fake ACP
// agent. It is delivered through the HarnessSpec env the coordinator hands
// the runner (fakeSpawner.engineEnv), so ONLY the spawned engine sees it —
// the parent test process never does, and TestHelperFakeACPAgent below is an
// ordinary skipped test there.
const fakeACPAgentEnv = "CTXLOOM_TEST_FAKE_ACP_AGENT"

// fakeACPMarker is the text the fake agent emits only AFTER its permission
// request is answered with an allow option. Its presence downstream is proof
// the whole round trip resolved; its absence is the hang.
const fakeACPMarker = "ACP-PERMISSION-RESOLVED-MARKER-8f21c4"

// fakeACPDeniedPrefix is what the fake agent emits instead when the outcome
// is anything but an allow — so a DENIED/CANCELLED resolution is a distinct,
// asserted-on payload rather than an indistinguishable silence.
const fakeACPDeniedPrefix = "ACP-PERMISSION-REFUSED:"

// TestHelperFakeACPAgent is the spawned-process entry point (the standard
// re-exec-the-test-binary helper pattern). In the parent test process the
// env marker is absent and it skips.
func TestHelperFakeACPAgent(t *testing.T) {
	if os.Getenv(fakeACPAgentEnv) != "1" {
		t.Skip("helper process only: runs as the spawned fake ACP agent")
	}
	serveFakeACPAgent(os.Stdin, os.Stdout)
	// Exit before the testing framework can write PASS/ok to stdout — that
	// stream is the JSON-RPC wire.
	os.Exit(0)
}

// fakeACPBackend builds the REAL generic ACP driver pointed at this test
// binary re-executed as the fake agent.
func fakeACPBackend() agent.StructuredChat {
	return acp.NewChatDriver(acp.ACPConfig{
		BinaryPath: os.Args[0],
		Args:       []string{"-test.run=TestHelperFakeACPAgent", "-test.timeout=0", "--"},
	})
}

// --- the fake agent (spawned process) ---

type fakeACPFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type fakeACPPeer struct {
	dec *json.Decoder
	w   io.Writer

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[int64]chan fakeACPFrame
	nextID    int64
}

// serveFakeACPAgent answers the ACP handshake, then — on every
// session/prompt — announces a tool call, ASKS the client for permission,
// and only after the client's answer emits its message and completes the
// turn. That ordering is the whole point: a client that never answers leaves
// the turn (and the run) parked forever, exactly as observed live.
func serveFakeACPAgent(r io.Reader, w io.Writer) {
	p := &fakeACPPeer{dec: json.NewDecoder(r), w: w, pending: make(map[int64]chan fakeACPFrame)}
	for {
		var m fakeACPFrame
		if err := p.dec.Decode(&m); err != nil {
			return
		}
		switch {
		case m.Method == "initialize":
			_ = p.respond(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}})
		case m.Method == "session/new":
			_ = p.respond(m.ID, map[string]any{"sessionId": "fake-acp-session-1"})
		case m.Method == "session/prompt":
			go p.servePrompt(m.ID)
		case m.Method != "" && len(m.ID) > 0:
			_ = p.respond(m.ID, nil)
		case m.Method != "":
			// notification (session/cancel): ignore
		case len(m.ID) > 0:
			p.routeResponse(m)
		}
	}
}

func (p *fakeACPPeer) servePrompt(promptID json.RawMessage) {
	_ = p.notify("session/update", json.RawMessage(`{"sessionId":"fake-acp-session-1","update":{"sessionUpdate":"tool_call","toolCallId":"tc-send","title":"mcp__ctxloom__agent_send","kind":"other","status":"pending","rawInput":{"to":"parent"}}}`))

	resp := p.requestPermission()
	text := fakeACPDeniedPrefix + string(resp)
	var decoded struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionId string `json:"optionId"`
		} `json:"outcome"`
	}
	if json.Unmarshal(resp, &decoded) == nil && decoded.Outcome.Outcome == "selected" && decoded.Outcome.OptionId == "allow-1" {
		text = fakeACPMarker
	}
	chunk, _ := json.Marshal(map[string]any{
		"sessionId": "fake-acp-session-1",
		"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}},
	})
	_ = p.notify("session/update", chunk)
	_ = p.respond(promptID, map[string]any{"stopReason": "end_turn"})
}

func (p *fakeACPPeer) requestPermission() json.RawMessage {
	id := atomic.AddInt64(&p.nextID, 1)
	ch := make(chan fakeACPFrame, 1)
	p.pendingMu.Lock()
	p.pending[id] = ch
	p.pendingMu.Unlock()

	params, _ := json.Marshal(map[string]any{
		"sessionId": "fake-acp-session-1",
		"toolCall":  map[string]any{"toolCallId": "tc-send", "title": "mcp__ctxloom__agent_send", "kind": "other"},
		"options": []map[string]any{
			{"optionId": "allow-1", "kind": "allow_once", "name": "Allow"},
			{"optionId": "reject-1", "kind": "reject_once", "name": "Reject"},
		},
	})
	_ = p.writeFrame(fakeACPFrame{ID: json.RawMessage(strconv.FormatInt(id, 10)), Method: "session/request_permission", Params: params})
	m := <-ch
	if len(m.Result) > 0 {
		return m.Result
	}
	return json.RawMessage(`{"error":` + string(m.Error) + `}`)
}

func (p *fakeACPPeer) routeResponse(m fakeACPFrame) {
	var id int64
	if json.Unmarshal(m.ID, &id) != nil {
		return
	}
	p.pendingMu.Lock()
	ch := p.pending[id]
	delete(p.pending, id)
	p.pendingMu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (p *fakeACPPeer) respond(id json.RawMessage, result any) error {
	raw := json.RawMessage("null")
	if result != nil {
		if data, err := json.Marshal(result); err == nil {
			raw = data
		}
	}
	return p.writeFrame(fakeACPFrame{ID: id, Result: raw})
}

func (p *fakeACPPeer) notify(method string, params json.RawMessage) error {
	return p.writeFrame(fakeACPFrame{Method: method, Params: params})
}

func (p *fakeACPPeer) writeFrame(m fakeACPFrame) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, werr := p.w.Write(data)
	return werr
}

// --- the reproduction ---

// newACPLivePathCoordinator wires a coordinator whose one agent spawns the
// real ACP driver against the fake agent subprocess.
func newACPLivePathCoordinator(t *testing.T, perm string) (*Coordinator, *fakeSpawner) {
	t.Helper()
	resetStrictness(t)
	sp := newFakeSpawner(map[string]fakeAgent{
		"worker": {perm: perm, runtime: "host", profiles: []string{"p1"}, viaStartRun: true, backend: "acp"},
	}, nil)
	sp.nextBackend = fakeACPBackend
	sp.engineWorkDir = t.TempDir()
	sp.engineEnv = map[string]string{fakeACPAgentEnv: "1"}
	return newTestCoordinator(t, sp, nil), sp
}

// TestACPLivePath_ForwardedPermissionResolves is the icy-value regression
// test: a delegated child running the REAL ACP driver against a REAL agent
// subprocess asks for permission mid-turn, and the coordinator's bypass
// ladder must answer it — with the child's own post-permission output as the
// payload proof, never a "the call returned nil" pin.
func TestACPLivePath_ForwardedPermissionResolves(t *testing.T) {
	c, _ := newACPLivePathCoordinator(t, "bypass")

	out, err := c.AgentRun(t.Context(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	// The child's turn cannot complete until its permission request is
	// answered — so reaching Idle IS the round trip completing.
	require.Eventually(t, func() bool { return rosterState(c, out.Harp) == StateIdle },
		30*time.Second, 50*time.Millisecond,
		"the child's turn never completed: its forwarded permission request was never resolved (icy-value)")

	entries := readApprovalAudit(t, c)
	require.NotEmpty(t, entries, "serveApproval was never reached: no approval fact was journaled")
	assert.Equal(t, "auto_accept", entries[0].Detail["action"])
	assert.Equal(t, "granted", entries[0].Detail["resolution"])
}

// TestACPLivePath_ChildResultBridgesToParentMailbox is the blunt-whiff
// payload proof on the SAME full stack: the child's model never calls
// agent_send (this fake agent has no MCP client at all — exactly the
// property that made the old StructuredChat-gated bridge dead code), yet the
// parent's mailbox carries the child's own words. Asserted on the CONTENT
// the child emitted, not on a call returning nil.
func TestACPLivePath_ChildResultBridgesToParentMailbox(t *testing.T) {
	c, _ := newACPLivePathCoordinator(t, "bypass")

	_, err := c.AgentRun(t.Context(), ownerIdentity(), "worker", "do the thing", "", "")
	require.NoError(t, err)

	msgs := recvBody(t, c, fakeACPMarker, 30*time.Second)
	require.Len(t, msgs, 1, "the parent must receive the child's result without the child choosing to report")
	assert.Equal(t, "result", msgs[0].Kind)
}

// TestInjectMCPSocketEnv covers the codex-child reach-back fix: the ctxloom
// forwarder MCP entry gets the runner's socket in its OWN declared env, so
// delivery does not depend on the engine adapter propagating ambient env
// (codex-acp does not).
func TestInjectMCPSocketEnv(t *testing.T) {
	servers := []agent.ChatMCPServer{
		{Name: agent.MCPServerName, Command: "ctxloom", Args: []string{"mcp"}},
		{Name: "other", Command: "x", Env: map[string]string{"K": "v"}},
	}
	injectMCPSocketEnv(servers, "/run/ctxloom/local/mcp-1.sock")
	assert.Equal(t, "/run/ctxloom/local/mcp-1.sock", servers[0].Env[EnvMCPSocket],
		"the ctxloom forwarder entry must carry the socket in its own env")
	assert.NotContains(t, servers[1].Env, EnvMCPSocket, "only the ctxloom entry is stamped")

	// No socket (bare/degraded runner): nothing injected.
	bare := []agent.ChatMCPServer{{Name: agent.MCPServerName}}
	injectMCPSocketEnv(bare, "")
	assert.NotContains(t, bare[0].Env, EnvMCPSocket)

	// A user override is never clobbered.
	override := []agent.ChatMCPServer{{Name: agent.MCPServerName, Env: map[string]string{EnvMCPSocket: "/custom"}}}
	injectMCPSocketEnv(override, "/run/ctxloom/local/mcp-1.sock")
	assert.Equal(t, "/custom", override[0].Env[EnvMCPSocket])
}

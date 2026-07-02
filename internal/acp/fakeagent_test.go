package acp

import (
	"encoding/json"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// rpcMessage mirrors the JSON-RPC wire frame. Kept LOCAL: the fake agent is
// deliberately independent of the production codec (internal/acp/jsonrpc) so a
// codec bug cannot hide by being symmetric on both ends of a test.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpc.Error  `json:"error,omitempty"`
}

// marshalResult renders a response result, defaulting to JSON null.
func marshalResult(result any) json.RawMessage {
	if result == nil {
		return json.RawMessage("null")
	}
	data, err := json.Marshal(result)
	if err != nil {
		return json.RawMessage("null")
	}
	return data
}

// fakeAgent is a synthetic ACP agent for driving the client end-to-end over
// in-memory pipes — no subprocess, no auth. It is a small async JSON-RPC peer
// (deliberately separate from the production rpcConn so a test can script
// re-entrant callbacks like session/request_permission during a session/prompt).
type fakeAgent struct {
	dec     *json.Decoder
	w       io.Writer
	writeMu sync.Mutex

	// requests carries inbound CLIENT requests (initialize / session/new /
	// session/prompt) to the script; notifications carries client notifications
	// (session/cancel). Both are closed-safe via the read loop's exit.
	requests      chan rpcMessage
	notifications chan rpcMessage

	pendingMu sync.Mutex
	pending   map[int64]chan rpcMessage
	nextID    int64
}

func newFakeAgent(r io.Reader, w io.Writer) *fakeAgent {
	a := &fakeAgent{
		dec:           json.NewDecoder(r),
		w:             w,
		requests:      make(chan rpcMessage, 16),
		notifications: make(chan rpcMessage, 16),
		pending:       make(map[int64]chan rpcMessage),
	}
	go a.readLoop()
	return a
}

// readLoop routes client frames: requests to the script, notifications to their
// channel, and responses (to our request_permission) back to the waiting caller.
func (a *fakeAgent) readLoop() {
	defer close(a.requests)
	for {
		var m rpcMessage
		if err := a.dec.Decode(&m); err != nil {
			return
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			a.requests <- m
		case m.Method != "":
			select {
			case a.notifications <- m:
			default:
			}
		case len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) == nil {
				a.pendingMu.Lock()
				ch := a.pending[id]
				a.pendingMu.Unlock()
				if ch != nil {
					ch <- m
				}
			}
		}
	}
}

func (a *fakeAgent) writeFrame(m rpcMessage) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_, werr := a.w.Write(data)
	return werr
}

// respond answers a client request with a result.
func (a *fakeAgent) respond(id json.RawMessage, result any) error {
	return a.writeFrame(rpcMessage{ID: id, Result: marshalResult(result)})
}

// sessionUpdate sends a session/update notification wrapping the given raw update
// object (e.g. `{"sessionUpdate":"agent_message_chunk",...}`).
func (a *fakeAgent) sessionUpdate(sessionID, updateJSON string) error {
	params := json.RawMessage(`{"sessionId":` + strconv.Quote(sessionID) + `,"update":` + updateJSON + `}`)
	return a.writeFrame(rpcMessage{Method: "session/update", Params: params})
}

// requestPermission sends a session/request_permission request to the client and
// blocks until the client's response frame comes back.
func (a *fakeAgent) requestPermission(sessionID string, options any) rpcMessage {
	id := atomic.AddInt64(&a.nextID, 1)
	ch := make(chan rpcMessage, 1)
	a.pendingMu.Lock()
	a.pending[id] = ch
	a.pendingMu.Unlock()

	params, _ := json.Marshal(map[string]any{
		"sessionId": sessionID,
		"options":   options,
		"toolCall":  map[string]any{"toolCallId": "tc1", "title": "run"},
	})
	_ = a.writeFrame(rpcMessage{Method: "session/request_permission", ID: json.RawMessage(strconv.FormatInt(id, 10)), Params: params})
	return <-ch
}

// serveHandshake answers initialize + session/new and returns the session id it
// handed back.
func (a *fakeAgent) serveHandshake(t *testing.T) string {
	t.Helper()
	initReq := <-a.requests
	require.Equal(t, "initialize", initReq.Method)
	require.NoError(t, a.respond(initReq.ID, map[string]any{"protocolVersion": 1}))

	newReq := <-a.requests
	require.Equal(t, "session/new", newReq.Method)
	const sid = "sess-1"
	require.NoError(t, a.respond(newReq.ID, map[string]any{"sessionId": sid}))
	return sid
}

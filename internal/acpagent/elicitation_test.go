package acpagent

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// startServerWithHandle is startServer's sibling that also hands the test the
// live *Server, so a test can drive an agent→client primitive
// (forwardElicitation) DIRECTLY at its own seam. The elicitation forward has
// no IR SOURCE to trigger it end-to-end yet — agent.ChatEvent carries no
// elicitation variant, so that cross-module IR carrier plus its client-role
// producer are DEFERRED (see the feature report). What IS implemented and
// tested here is the server-side client-request forward machinery, reusing
// s.conn.Call exactly as forwardPermission/forwardTerminal do.
func startServerWithHandle(t *testing.T, open ChatOpener) (*testClient, *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	s := &Server{open: open, ctx: ctx, sessions: make(map[api.SessionId]*session)}
	s.conn = jsonrpc.NewConn(ctx, serverR, serverW, nil, s)
	s.conn.Start(ctx)
	t.Cleanup(func() { cancel(); _ = clientW.Close(); _ = serverW.Close(); s.closeAllSessions() })

	c := &testClient{
		t:         t,
		w:         clientW,
		responses: make(chan frame, 32),
		updates:   make(chan frame, 64),
		requests:  make(chan frame, 8),
	}
	go func() {
		scan := bufio.NewScanner(clientR)
		scan.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for scan.Scan() {
			var f frame
			if json.Unmarshal(scan.Bytes(), &f) != nil {
				continue
			}
			switch {
			case f.Method != "" && len(f.ID) > 0:
				c.requests <- f
			case f.Method != "":
				c.updates <- f
			default:
				c.responses <- f
			}
		}
		close(c.responses)
	}()
	return c, s
}

// TestServe_Elicitation_ForwardsToClient proves the agent→client elicitation
// forward: when the connected client advertised elicitation at initialize,
// forwardElicitation issues an elicitation/create REQUEST to the client
// (reusing s.conn.Call, the same machinery forwardPermission/forwardTerminal
// use) and returns the client's decoded response.
func TestServe_Elicitation_ForwardsToClient(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c, s := startServerWithHandle(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	// Client advertises form-based elicitation at initialize.
	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{"elicitation":{"form":{}}}}`))
	require.Nil(t, resp.Error)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	sess := s.lookup(api.SessionId(newResp.SessionId))
	require.NotNil(t, sess)

	// Drive the forward primitive OFF the read loop; the client must see an
	// elicitation/create request and its response must come back decoded.
	done := make(chan api.UnstableCreateElicitationResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		req := api.UnstableCreateElicitationRequest{Form: &api.UnstableCreateElicitationForm{
			Message:         "Pick a branch",
			RequestedSchema: api.UnstableElicitationSchema{},
		}}
		r, err := s.forwardElicitation(sess, req)
		if err != nil {
			errCh <- err
			return
		}
		done <- r
	}()

	req := c.waitRequest(api.ClientMethodElicitationCreate)
	var got struct {
		Mode    string `json:"mode"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(req.Params, &got))
	assert.Equal(t, "form", got.Mode)
	assert.Equal(t, "Pick a branch", got.Message)

	c.respond(req, `{"action":"accept","content":{"branch":"main"}}`)

	select {
	case err := <-errCh:
		t.Fatalf("forwardElicitation errored: %v", err)
	case r := <-done:
		require.NotNil(t, r.Accept, "accept variant should decode")
		assert.Equal(t, "accept", r.Accept.Action)
		assert.Equal(t, "main", r.Accept.Content["branch"])
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for elicitation response")
	}
}

// TestServe_Elicitation_RefusedWhenClientLacksCapability proves the honesty
// discipline: a client that never advertised elicitation gets a clear error,
// never a silent no-op, when the agent tries to elicit.
func TestServe_Elicitation_RefusedWhenClientLacksCapability(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c, s := startServerWithHandle(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)
	var newResp struct {
		SessionId string `json:"sessionId"`
	}
	require.NoError(t, json.Unmarshal(resp.Result, &newResp))
	sess := s.lookup(api.SessionId(newResp.SessionId))
	require.NotNil(t, sess)

	req := api.UnstableCreateElicitationRequest{Form: &api.UnstableCreateElicitationForm{Message: "x"}}
	_, err := s.forwardElicitation(sess, req)
	require.Error(t, err, "an elicitation to a non-advertising client must be refused, never silently dropped")
}


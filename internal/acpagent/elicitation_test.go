//go:build acp_elicitation

// EXCLUDED FROM THE BUILD ON PURPOSE, and a t.Skip could not do this job: the
// test drives Server.forwardElicitation, a production method that DOES NOT
// EXIST. A skip runs after compilation, so a skipped version of this file would
// still red the whole package.
//
// WHY IT IS IN THE TREE AT ALL. ACP elicitation has no end-to-end path —
// agent.ChatEvent/ChatMessage carry no elicitation variant, so nothing can
// ORIGINATE an elicitation, and the forward primitive was never written. This
// file is the SPEC for the primitive: it drives the server-side forward at its
// own seam, mirroring forwardPermission/forwardTerminal via s.conn.Call, and it
// says what the missing method must satisfy. It nearly died twice — its branch
// was deleted and the commit survived only as a dangling object until a backlog
// audit rescued it (task much-jiffy).
//
// THE COST, stated so it does not read later as an oversight: nothing compiles
// this file, so it WILL drift against the tree as this package moves, exactly
// as it already did (jsonrpc.NewConn lost a parameter while it sat on a branch,
// and that was repaired on the way in). It is preserved and greppable, not
// maintained. Build it with `-tags acp_elicitation` to see what it needs.
//
// TO REVIVE: write Server.forwardElicitation, add the elicitation variant to
// the agent IR, then delete this build tag.

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
	s.conn = jsonrpc.NewConn(serverR, serverW, nil, s)
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
	// SKIPPED, NOT DELETED, and the reason is the finding. ACP elicitation has
	// no end-to-end path: agent.ChatEvent/ChatMessage carry no elicitation
	// variant, so nothing in the tree can ORIGINATE one, and the GREEN forward
	// primitive was never written. This test drives the server-side forward at
	// its own seam and documents the shape the primitive must satisfy.
	//
	// It is kept because it nearly died twice: its original branch was deleted
	// and the commit survived only as a dangling object until a backlog audit
	// rescued it (task much-jiffy). Unskip it when the IR carrier lands.
	t.Skip("ACP elicitation has no IR source yet — agent.ChatEvent carries no elicitation variant; see much-jiffy")

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
	// SKIPPED, NOT DELETED, and the reason is the finding. ACP elicitation has
	// no end-to-end path: agent.ChatEvent/ChatMessage carry no elicitation
	// variant, so nothing in the tree can ORIGINATE one, and the GREEN forward
	// primitive was never written. This test drives the server-side forward at
	// its own seam and documents the shape the primitive must satisfy.
	//
	// It is kept because it nearly died twice: its original branch was deleted
	// and the commit survived only as a dangling object until a backlog audit
	// rescued it (task much-jiffy). Unskip it when the IR carrier lands.
	t.Skip("ACP elicitation has no IR source yet — agent.ChatEvent carries no elicitation variant; see much-jiffy")

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

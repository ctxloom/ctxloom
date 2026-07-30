package acpagent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// dialAndCallFsUpstream stands in for internal/acp's dialFsUpstream +
// chatSession.handleFsRead — the engine-conversation-side consumer that
// actually exists in a SEPARATE process in production (the self-invoking
// "ctxloom llm serve" subprocess). Here it just proves the acpagent-side
// listener (fsupstream.go) is reachable at addr and relays a real request.
func dialAndCallFsUpstream(t *testing.T, addr, method string, params any) (json.RawMessage, error) {
	t.Helper()
	conn, err := net.Dial("unix", addr)
	require.NoError(t, err)
	defer conn.Close()
	rpc := jsonrpc.NewConn(conn, conn, jsonrpc.CloserFunc(conn.Close), noopHandler{})
	rpc.Start(context.Background())
	var result json.RawMessage
	callErr := rpc.Call(context.Background(), method, params, &result)
	return result, callErr
}

type noopHandler struct{}

func (noopHandler) HandleRequest(_ context.Context, method string, _ json.RawMessage, reply func(any, *jsonrpc.Error)) {
	reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unexpected " + method})
}
func (noopHandler) HandleNotification(context.Context, string, json.RawMessage) {}

// TestServe_FsUpstream_OfferedWhenClientDeclaresFs proves the acpagent-role
// half of B5 (gap G14) end to end: when the connected editor declares
// fs/read_text_file at initialize, session/new stands up a real local
// listener, hands its address to the ChatOpener via OpenRequest.
// FsUpstreamAddr, and a request dialed against that address really reaches
// the CONNECTED EDITOR (relayed over the server's own connection) and the
// editor's answer really flows back through it.
func TestServe_FsUpstream_OfferedWhenClientDeclaresFs(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()

	var gotAddr string
	c := startServer(t, func(ctx context.Context, req OpenRequest) (*EngineChat, error) {
		gotAddr = req.FsUpstreamAddr
		return eng.chat(""), nil
	})

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{"fs":{"readTextFile":true,"writeTextFile":true}}}`))
	require.Nil(t, resp.Error)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	require.NotEmpty(t, gotAddr, "the editor declared fs — OpenRequest must carry a real fs-upstream address")

	// Drive a read through the socket as the (in production, far-process)
	// engine conversation would, and answer it as the real editor from this
	// test's client side.
	done := make(chan struct{})
	var readResult json.RawMessage
	var readErr error
	go func() {
		defer close(done)
		readResult, readErr = dialAndCallFsUpstream(t, gotAddr, api.ClientMethodFsReadTextFile,
			map[string]any{"path": "/proj/unsaved.go"})
	}()

	req := c.waitRequest(api.ClientMethodFsReadTextFile)
	var params struct {
		Path string `json:"path"`
	}
	require.NoError(t, json.Unmarshal(req.Params, &params))
	assert.Equal(t, "/proj/unsaved.go", params.Path)
	c.respond(req, `{"content":"EDITOR BUFFER CONTENT"}`)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the fs-upstream round trip")
	}
	require.NoError(t, readErr)
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(readResult, &got))
	assert.Equal(t, "EDITOR BUFFER CONTENT", got.Content)
}

// TestServe_FsUpstream_NotOfferedWhenClientDeclinesFs: an editor that never
// declares the fs capability gets no listener at all — starting one nothing
// could ever answer would be pointless, not merely unused, and
// OpenRequest.FsUpstreamAddr stays "" so OpenEngineSession never even
// considers forwarding it.
func TestServe_FsUpstream_NotOfferedWhenClientDeclinesFs(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()

	var gotAddr string
	seen := make(chan struct{}, 1)
	c := startServer(t, func(ctx context.Context, req OpenRequest) (*EngineChat, error) {
		gotAddr = req.FsUpstreamAddr
		seen <- struct{}{}
		return eng.chat(""), nil
	})

	resp, _ := c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	require.Nil(t, resp.Error)
	resp, _ = c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	<-seen
	assert.Empty(t, gotAddr, "no fs capability declared -> no fs-upstream address offered at all")
}

// TestFsUpstream_CloseRemovesTheTempDir pins U014-F09: startFsUpstream mints a
// private temp DIRECTORY to hold its socket, but Close removed only the socket
// FILE — so every fs-upstream session left an empty ctxloom-acp-fs-* directory
// behind in TMPDIR for the life of the machine. Both failure paths inside
// startFsUpstream already used os.RemoveAll on the same directory, so the
// success path was the odd one out.
func TestFsUpstream_CloseRemovesTheTempDir(t *testing.T) {
	s := &Server{ctx: context.Background()}
	s.setClientFs(api.FileSystemCapabilities{ReadTextFile: true})

	f := s.startFsUpstream()
	require.NotNil(t, f, "the editor declared readTextFile — a listener must be stood up")
	dir := filepath.Dir(f.Addr())
	require.DirExists(t, dir)

	require.NoError(t, f.Close())

	assert.NoFileExists(t, f.Addr(), "the socket file must be gone")
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err),
		"Close must remove the temp DIRECTORY it created, not just the socket inside it; %s still exists", dir)
}

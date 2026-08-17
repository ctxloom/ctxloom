package acp

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
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// fakeFsUpstream is a minimal stand-in for internal/acpagent's real fs-upstream
// listener (which relays to a real connected editor): it answers
// fs/read_text_file with a FIXED, test-controlled body and records every
// fs/write_text_file call it receives — enough to prove chatSession is
// actually talking to THIS socket and not touching local disk at all.
type fakeFsUpstream struct {
	ln         net.Listener
	readReturn string
	writes     chan api.WriteTextFileRequest
}

func startFakeFsUpstream(t *testing.T, readReturn string) *fakeFsUpstream {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "fs.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	f := &fakeFsUpstream{ln: ln, readReturn: readReturn, writes: make(chan api.WriteTextFileRequest, 4)}
	go f.acceptLoop()
	t.Cleanup(func() { _ = ln.Close() })
	return f
}

func (f *fakeFsUpstream) addr() string { return f.ln.Addr().String() }

func (f *fakeFsUpstream) acceptLoop() {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return
		}
		jsonrpc.NewConn(conn, conn, jsonrpc.CloserFunc(conn.Close), f).Start(context.Background())
	}
}

func (f *fakeFsUpstream) HandleRequest(_ context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.ClientMethodFsReadTextFile:
		reply(api.ReadTextFileResponse{Content: f.readReturn}, nil)
	case api.ClientMethodFsWriteTextFile:
		var req api.WriteTextFileRequest
		_ = json.Unmarshal(params, &req)
		f.writes <- req
		reply(api.WriteTextFileResponse{}, nil)
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "unexpected method " + method})
	}
}

func (f *fakeFsUpstream) HandleNotification(context.Context, string, json.RawMessage) {}

// TestChat_FsUpstream_ReadChainsToEditor is the headline proof: on
// the host axis (req.Env carries fsUpstreamEnvVar), fs/read_text_file returns
// the CONNECTED EDITOR's answer — standing in for an unsaved buffer's live
// content — NOT local disk, even though a file exists on disk at the exact
// same path with DIFFERENT content. Getting this backwards would silently
// serve the engine stale/wrong content with no error, exactly the failure
// mode this slice exists to prevent.
func TestChat_FsUpstream_ReadChainsToEditor(t *testing.T) {
	// The file lives INSIDE the session workspace: T13's confinement
	// (fsconfine.go) is decided before the upstream branch, so a fixture
	// outside the root would be denied before the axis under test is ever
	// exercised. Which axis answers is what this test measures.
	workspace := t.TempDir()
	tmpFile := filepath.Join(workspace, "buffer.go")
	require.NoError(t, os.WriteFile(tmpFile, []byte("DISK: stale committed content\n"), 0o644))

	up := startFakeFsUpstream(t, "EDITOR: live unsaved buffer content")

	h := startChat(t, agent.ChatRequest{WorkDir: workspace, Env: map[string]string{fsUpstreamEnvVar: up.addr()}})
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": tmpFile})
	require.Nil(t, resp.Error)
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "EDITOR: live unsaved buffer content", got.Content, "must return the EDITOR's content, not disk's")
	assert.NotContains(t, got.Content, "DISK", "must never leak local disk content on the host axis")

	closeChat(t, h, events)
}

// TestChat_FsUpstream_WriteChainsToEditor: fs/write_text_file on the host
// axis relays to the editor instead of touching local disk — the editor's
// buffer is authoritative for writes too, on this axis.
func TestChat_FsUpstream_WriteChainsToEditor(t *testing.T) {
	// EvalSymlinks so the expected path below is the same REAL path T13's
	// confinement resolves to before relaying (fsconfine.go) — on a platform
	// where TMPDIR is a symlink the two spellings differ.
	workspace, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	tmpFile := filepath.Join(workspace, "buffer.go") // deliberately never created
	up := startFakeFsUpstream(t, "")

	h := startChat(t, agent.ChatRequest{WorkDir: workspace, Env: map[string]string{fsUpstreamEnvVar: up.addr()}})
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsWriteTextFile, map[string]any{"path": tmpFile, "content": "new content"})
	require.Nil(t, resp.Error)

	select {
	case w := <-up.writes:
		assert.Equal(t, tmpFile, w.Path)
		assert.Equal(t, "new content", w.Content)
	case <-time.After(5 * time.Second):
		t.Fatal("fake fs-upstream never received the write")
	}
	_, statErr := os.Stat(tmpFile)
	assert.True(t, os.IsNotExist(statErr), "the write must land on the EDITOR, never local disk, on the host axis")

	closeChat(t, h, events)
}

// TestChat_FsUpstream_AbsentServesLocalDisk pins the OTHER two axes'
// behavior (worktree and container — see operations.shouldChainFsUpstream,
// which never sets this env var for either): with no fs-upstream address at
// all, fs/read_text_file and fs/write_text_file behave EXACTLY as before
// this slice — plain local disk, no dial attempted, no fake listener even
// running.
func TestChat_FsUpstream_AbsentServesLocalDisk(t *testing.T) {
	workspace := t.TempDir()
	tmpFile := filepath.Join(workspace, "worktree-file.go")
	require.NoError(t, os.WriteFile(tmpFile, []byte("WORKTREE: real content\n"), 0o644))

	h := startChat(t, agent.ChatRequest{WorkDir: workspace}) // no Env at all
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": tmpFile})
	require.Nil(t, resp.Error)
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "WORKTREE: real content\n", got.Content, "worktree/container axes: local disk (the worktree checkout or the same-path container mount) is the correct read, unchanged")

	closeChat(t, h, events)
}

// TestChat_FsUpstream_DialFailureDegradesToLocalDisk: an unreachable
// fs-upstream address (e.g. the acpagent-side listener already tore down)
// must never fail the whole conversation — it degrades to local disk, the
// same fault-tolerant posture context assembly failures already get
// elsewhere in this codebase.
func TestChat_FsUpstream_DialFailureDegradesToLocalDisk(t *testing.T) {
	workspace := t.TempDir()
	tmpFile := filepath.Join(workspace, "f.go")
	require.NoError(t, os.WriteFile(tmpFile, []byte("disk content\n"), 0o644))

	bogus := filepath.Join(t.TempDir(), "does-not-exist.sock")
	h := startChat(t, agent.ChatRequest{WorkDir: workspace, Env: map[string]string{fsUpstreamEnvVar: bogus}})
	events := collect(h.out)
	h.fa.serveHandshake(t)

	resp := l0CallClient(h.fa, api.ClientMethodFsReadTextFile, map[string]any{"path": tmpFile})
	require.Nil(t, resp.Error)
	var got api.ReadTextFileResponse
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	assert.Equal(t, "disk content\n", got.Content, "a dial failure must degrade to local disk, never fail the conversation")

	closeChat(t, h, events)
}

// closeChat ends the harness's input and waits for Chat to return cleanly,
// draining events so the deferred close(out) inside Chat cannot block.
func closeChat(t *testing.T, h *chatHarness, events func() []agent.ChatEvent) {
	t.Helper()
	close(h.in)
	select {
	case err := <-h.chatErr:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Chat to return after closing input")
	}
	events()
}

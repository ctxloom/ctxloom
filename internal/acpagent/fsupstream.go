package acpagent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// This file is B5's agent-role half (gap G14): standing up the local
// reach-back listener that lets a HOST-axis session's engine conversation
// chain fs/read_text_file and fs/write_text_file UPSTREAM to the connected
// editor, instead of local disk. See operations.OpenRequest.FsUpstreamAddr's
// doc for the full "one rule" (host chains, worktree/container never do) and
// internal/acp/fsupstream.go for the consuming half inside the engine
// conversation.
//
// WHY A SEPARATE LOCAL SOCKET AND NOT THE EXISTING PERMISSION-FORWARDING
// CHANNEL: every engine conversation runs inside a SEPARATE "ctxloom llm
// serve <backend>" subprocess (the self-invoking plugin pattern,
// internal/lm/grpc) — session.go's ACP client driver executes THERE, not in
// this process. Permission forwarding already crosses that boundary, but
// through agent.ChatEvent/ChatMessage's fixed protobuf oneofs (a Server
// change here), which is explicitly out of scope for this slice (owned by a
// concurrent one). A plain unix socket, with its address forwarded through
// the EXISTING generic env map (agent.ChatRequest.Env — already a
// map[string]string, no new proto field needed) sidesteps that entirely:
// the subprocess dials this socket directly and speaks the SAME
// internal/acp/jsonrpc codec used everywhere else in this protocol, so no
// new wire format exists either.

// clientFs is the connected editor's advertised fs capabilities, captured
// once at initialize (see handleInitialize) so a session only offers
// upstream chaining when the editor actually declared it can serve
// fs/read_text_file — starting a listener nothing could ever answer would
// be pointless, not merely unused.
func (s *Server) setClientFs(fs api.FileSystemCapabilities) {
	s.mu.Lock()
	s.clientFs = fs
	s.mu.Unlock()
}

func (s *Server) getClientFs() api.FileSystemCapabilities {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientFs
}

// fsUpstreamListener is one session's local reach-back socket: accepted
// connections (expected: exactly one, from that session's "ctxloom llm
// serve" subprocess) relay fs/read_text_file and fs/write_text_file to the
// real connected editor via the Server's own connection.
type fsUpstreamListener struct {
	ln     net.Listener
	addr   string
	cancel context.CancelFunc
	closed atomic.Bool
	// dir is the private temp directory startFsUpstream created to hold the
	// socket. It belongs to this listener alone, so Close owns removing the
	// whole directory — removing only the socket inside it leaks an empty
	// directory into TMPDIR for every session that ever offered fs chaining.
	dir string
}

// Addr is the unix socket path a session's OpenRequest.FsUpstreamAddr carries.
func (f *fsUpstreamListener) Addr() string { return f.addr }

// Close stops accepting and removes the private temp directory holding the
// socket (idempotent).
func (f *fsUpstreamListener) Close() error {
	if f == nil || f.closed.Swap(true) {
		return nil
	}
	f.cancel()
	err := f.ln.Close()
	_ = os.RemoveAll(f.dir)
	return err
}

// startFsUpstream stands up ONE session's fs-reach-back listener, or
// returns (nil, nil) when the connected editor never declared fs/
// read_text_file at initialize — there being nothing to chain to is not an
// error, just nothing offered (the session serves local disk, same as
// today). A listener-setup failure (e.g. no writable temp dir) also
// degrades to nil rather than refusing the whole session open — an
// unavailable OPTIMIZATION must never block a session the way a real
// isolation/config finding does.
func (s *Server) startFsUpstream() *fsUpstreamListener {
	if !s.getClientFs().ReadTextFile {
		return nil
	}
	dir, err := os.MkdirTemp("", "ctxloom-acp-fs-")
	if err != nil {
		clidiag.Warn("ctxloom", "acp agent: fs-upstream listener: %v (falling back to local disk fs for this session)", err)
		return nil
	}
	sockPath := filepath.Join(dir, "fs.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		clidiag.Warn("ctxloom", "acp agent: fs-upstream listener: %v (falling back to local disk fs for this session)", err)
		return nil
	}
	ctx, cancel := context.WithCancel(s.ctx)
	f := &fsUpstreamListener{ln: ln, addr: sockPath, cancel: cancel, dir: dir}
	go s.acceptFsUpstream(ctx, f)
	return f
}

// acceptFsUpstream serves accepted connections until ctx is cancelled or the
// listener closes (Close calling ln.Close unblocks a parked Accept). Every
// connection is wrapped as its own jsonrpc peer answering fs/read_text_file
// and fs/write_text_file by relaying to the editor over s.conn — the SAME
// connection forwardPermission already calls on, and jsonrpc.Conn is
// documented safe for concurrent Call.
func (s *Server) acceptFsUpstream(ctx context.Context, f *fsUpstreamListener) {
	for {
		conn, err := f.ln.Accept()
		if err != nil {
			return // listener closed (session teardown) — normal exit
		}
		jsonrpc.NewConn(conn, conn, jsonrpc.CloserFunc(conn.Close), &fsUpstreamHandler{server: s}).Start(ctx)
	}
}

// fsUpstreamHandler answers the two fs methods over one accepted local
// connection by relaying verbatim to the real editor. Any other inbound
// method is refused (this socket has exactly one purpose); notifications
// are dropped with a warning like every other unmodeled frame in this
// codebase.
type fsUpstreamHandler struct {
	server *Server
}

func (h *fsUpstreamHandler) HandleRequest(ctx context.Context, method string, params json.RawMessage, reply func(any, *jsonrpc.Error)) {
	switch method {
	case api.ClientMethodFsReadTextFile:
		var req api.ReadTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
			return
		}
		var resp api.ReadTextFileResponse
		if err := h.server.conn.Call(ctx, api.ClientMethodFsReadTextFile, req, &resp); err != nil {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()})
			return
		}
		reply(resp, nil)
	case api.ClientMethodFsWriteTextFile:
		var req api.WriteTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: err.Error()})
			return
		}
		var resp api.WriteTextFileResponse
		if err := h.server.conn.Call(ctx, api.ClientMethodFsWriteTextFile, req, &resp); err != nil {
			reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: err.Error()})
			return
		}
		reply(resp, nil)
	default:
		reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ctxloom acp: fs-upstream socket: method not supported: " + method})
	}
}

func (h *fsUpstreamHandler) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	clidiag.Warn("ctxloom", "acp agent: fs-upstream socket: dropping unexpected notification %q", method)
}

// openSessionWithFsUpstream wraps openSession with B5's fs-upstream setup:
// it stands up the listener (nil when unavailable/unneeded — see
// startFsUpstream), threads its address into the OpenRequest so
// OpenEngineSession can decide per-axis whether to actually forward it (see
// operations.OpenRequest.FsUpstreamAddr), and attaches the listener to the
// resulting session for teardown — or closes it immediately when
// openSession itself failed, so a failed session open never leaks the
// socket.
func (s *Server) openSessionWithFsUpstream(req OpenRequest, fixedID api.SessionId) (*session, *jsonrpc.Error) {
	fsUp := s.startFsUpstream()
	if fsUp != nil {
		req.FsUpstreamAddr = fsUp.Addr()
	}
	sess, rerr := s.openSession(req, fixedID)
	if rerr != nil {
		if err := fsUp.Close(); err != nil {
			clidiag.Warn("ctxloom", "acp agent: fs-upstream listener cleanup: %v", err)
		}
		return nil, rerr
	}
	sess.fsUpstream = fsUp
	return sess, nil
}

package acp

import (
	"context"
	"encoding/json"
	"net"

	"github.com/ctxloom/ctxloom/internal/acp/jsonrpc"
)

// This file is the engine-conversation half of fs reach-back: dialing the local
// fs-reach-back socket internal/acpagent's fs-upstream listener stands up
// (internal/acpagent/fsupstream.go), so chatSession's handleFsRead/
// handleFsWrite (session.go) can chain to the connected editor on the HOST
// axis instead of local disk. See operations.OpenRequest.FsUpstreamAddr's
// doc comment for the full "one rule" and exactly which axis forwards this
// address at all.

// fsUpstreamEnvVar mirrors operations.FsUpstreamEnvVar's EXACT string
// literal. internal/acp sits BELOW internal/operations in the import graph
// (the ACP client driver must not import back up to it — the same
// constraint the agent/isolation container-runtime constants already
// document for req.Runtime), so this is a second literal copy of the same
// constant, not an import. It is BOUND to the original by
// constants_binding_test.go (an external test package, which is not in that
// cycle) so a rename cannot silently reroute fs/* to local disk.
const fsUpstreamEnvVar = "CTXLOOM_ACP_FS_UPSTREAM"

// dialFsUpstream connects to the acpagent-side fs reach-back listener at
// addr (a unix socket path) and wraps it as a jsonrpc peer this side only
// ever CALLS on (fs/read_text_file, fs/write_text_file) — it never expects
// an inbound request of its own, hence the no-op handler.
func dialFsUpstream(ctx context.Context, addr string) (*jsonrpc.Conn, error) {
	conn, err := net.Dial("unix", addr)
	if err != nil {
		return nil, err
	}
	c := jsonrpc.NewConn(conn, conn, jsonrpc.CloserFunc(conn.Close), noopFsUpstreamHandler{})
	c.Start(ctx)
	return c, nil
}

// noopFsUpstreamHandler answers nothing: the fs-upstream socket is a
// client-only connection from this side (this process only ever issues
// Call; the acpagent side never sends a request back down it). Any
// unexpected inbound frame is dropped with a warning rather than a panic —
// this codebase's standard fault-tolerance posture for unmodeled frames.
type noopFsUpstreamHandler struct{}

func (noopFsUpstreamHandler) HandleRequest(_ context.Context, method string, _ json.RawMessage, reply func(any, *jsonrpc.Error)) {
	reply(nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "ctxloom acp: fs-upstream socket answers no inbound requests: " + method})
}

func (noopFsUpstreamHandler) HandleNotification(_ context.Context, method string, _ json.RawMessage) {
	warnf("acp: fs-upstream socket: dropping unexpected notification %q", method)
}

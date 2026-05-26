package cmd

import (
	"context"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// installSessionBindMiddleware wires a one-shot forward bind into the
// MCP receiving pipeline. The first method handled triggers a best-
// effort lookup of the current backend session and stores its ID in
// ~/.ctxloom/sessions/index.yaml against the active CTXLOOM_SESSION_HARP.
//
// This is forward-looking, NOT history archaeology: we ask the backend
// for the session we're already in, and we ask only once. After the
// bind, `ctxloom session distill <harp>` can find the transcript even
// if the LLM never triggered an automatic compact_session.
//
// Pre-release sessions and sessions whose backend doesn't expose
// history are silently un-bindable; their picker rows stay "(no
// summary)" and force-distill fails with a clear error.
func (s *ctxServer) installSessionBindMiddleware(server *mcp.Server) {
	server.AddReceivingMiddleware(s.sessionBindMiddleware())
}

func (s *ctxServer) sessionBindMiddleware() mcp.Middleware {
	var once sync.Once
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			once.Do(s.tryBindCurrentSession)
			return next(ctx, method, req)
		}
	}
}

// tryBindCurrentSession is the no-throw forward bind. Every failure
// path is silent: missing harp, missing backend, missing history,
// missing session, missing index entry, or already-bound entry all
// produce no log noise. The user only finds out something didn't bind
// when they later try `ctxloom session distill <harp>` and get a
// "harp has no session_id bound yet" error.
func (s *ctxServer) tryBindCurrentSession() {
	harp := os.Getenv("CTXLOOM_SESSION_HARP")
	if harp == "" || s.cfg == nil {
		return
	}
	backendName := s.cfg.LM.GetDefaultPlugin()
	if backendName == "" {
		backendName = "claude-code"
	}
	backend := backends.Get(backendName)
	if backend == nil {
		return
	}
	history := backend.History()
	if history == nil {
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	session, err := history.GetCurrentSession(wd)
	if err != nil || session == nil {
		return
	}
	mgr, err := sessions.Open("")
	if err != nil {
		return
	}
	entry, _ := mgr.Find(harp)
	if entry == nil || entry.SessionID != "" {
		return
	}
	_ = mgr.BindSession(harp, session.ID, "")
}

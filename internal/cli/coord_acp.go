package cli

import (
	"sync"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/mcp"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// acpCoordinator lazily hosts ONE runtime coordinator per `ctxloom acp`
// process, keyed by the project of the FIRST session that opens (ACP sessions
// share the editor's project). Each session/new mints its own owner
// credential on that shared coordinator. Standup is lazy — an ACP process
// that only ever chats (never delegates) still pays nothing until a session
// opens — and fault-tolerant: a standup failure warns and the session opens
// with no delegation reach-back (ACP's usual posture).
type acpCoordinator struct {
	mu    sync.Mutex
	c     *coord.Coordinator
	tried bool
}

func newACPCoordinator() *acpCoordinator { return &acpCoordinator{} }

// acpCoordinator is handed to operations.OpenEngineSession as an
// operations.EngineSessionCoordinator (acp_cmd.go), and its two methods live in
// different files here: SessionEnv below, WatchChildren in acp_children.go.
// This states the binding AT THE TYPE, so a signature change on either side
// names acpCoordinator and the missing method instead of only failing at
// whichever call site happens to pass it.
var _ operations.EngineSessionCoordinator = (*acpCoordinator)(nil)

// SessionEnv returns the coordinator reach-back pair (EnvCoordURL,
// EnvCoordCred) for one ACP session's
// engine env (host runtime — the loopback endpoint reaches the editor's cwd).
// Empty on any standup/mint failure (warned once). Exported: it implements
// operations.EngineSessionCoordinator (internal/operations/engine_session.go)
// so OpenEngineSession can call it without importing this package.
func (a *acpCoordinator) SessionEnv(cfg *config.Config, cwd, harp string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.c == nil {
		if a.tried {
			return nil
		}
		a.tried = true
		c, err := mcp.NewHostedCoordinator(cfg, cwd)
		if err != nil {
			clidiag.Warn("ctxloom", "acp: agent delegation unavailable (coordinator standup failed): %v", err)
			return nil
		}
		a.c = c
	}
	env, err := mcp.SessionOwnerEnv(a.c, harp, "host")
	if err != nil {
		clidiag.Warn("ctxloom", "acp: mint session credential: %v", err)
		return nil
	}
	return env
}

// coordinator returns the hosted coordinator, or nil when standup was never
// attempted (no session has opened yet) or already failed (warned once at
// the time — see sessionEnv). D3 (acp_children.go) uses this to decide
// whether a session gets the child-update push at all.
func (a *acpCoordinator) coordinator() *coord.Coordinator {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.c
}

func (a *acpCoordinator) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.c != nil {
		a.c.Close()
		a.c = nil
	}
}

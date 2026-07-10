package cli

import (
	"sync"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
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

// sessionEnv returns the coordinator reach-back trio for one ACP session's
// engine env (host runtime — the loopback endpoint reaches the editor's cwd).
// Empty on any standup/mint failure (warned once).
func (a *acpCoordinator) sessionEnv(cfg *config.Config, cwd, harp string) map[string]string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.c == nil {
		if a.tried {
			return nil
		}
		a.tried = true
		c, err := newHostedCoordinator(cfg, cwd)
		if err != nil {
			clidiag.Warn("ctxloom", "acp: agent delegation unavailable (coordinator standup failed): %v", err)
			return nil
		}
		a.c = c
	}
	env, err := sessionOwnerEnv(a.c, harp, "host")
	if err != nil {
		clidiag.Warn("ctxloom", "acp: mint session credential: %v", err)
		return nil
	}
	return env
}

func (a *acpCoordinator) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.c != nil {
		a.c.Close()
		a.c = nil
	}
}

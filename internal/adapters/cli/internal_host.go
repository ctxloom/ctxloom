package cli

import (
	"context"
	"sync"

	"github.com/ctxloom/ctxloom/internal/adapters/mcp"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// The coordinator a command's INTERNAL one-shot runs on when the process
// hosts none for a session of its own: a distill (`bundle distill`,
// `session distill`), init's auth probe and discovery launch. A project has
// ONE coordinator, owned by a session harp, so it is hosted for the FIRST
// one-shot the command starts — under that session's harp — and every later
// one-shot in the process rides the same coordinator (init's discovery
// launch after its probe). It is closed when the command's run ends
// (rootPersistentPostRunE). A project already owned by a live session
// refuses the host (coord.ErrStateOwned), and the one-shot with it: the
// runner is started and turned by a coordinator, and there is no second arm
// to drive one without.
var internalCoord struct {
	mu sync.Mutex
	c  *coord.Coordinator
}

// internalCoordinator is the command's internal coordinator, hosted on first
// use under ownerHarp.
func internalCoordinator(projectDir, ownerHarp string) (*coord.Coordinator, error) {
	internalCoord.mu.Lock()
	defer internalCoord.mu.Unlock()
	if internalCoord.c != nil {
		return internalCoord.c, nil
	}
	// The owner credential is for a session the command drives itself; an
	// internal one-shot's runner is stamped by StartOwnedRun.
	c, _, err := mcp.HostCoordinatorForSession(NewCoordinator, App(), projectDir, ownerHarp, coord.OwnerNonInteractive)
	if err != nil {
		return nil, err
	}
	internalCoord.c = c
	return c, nil
}

// internalRunHosts is the operations.RunHosts a command hands its one-shots.
func internalRunHosts() operations.RunHosts {
	return operations.RunHostFunc(func(_ context.Context, projectDir, ownerHarp string) (operations.RunHost, error) {
		return internalCoordinator(projectDir, ownerHarp)
	})
}

// closeInternalCoordinator ends the command's internal coordinator, if one
// was hosted. Idempotent.
func closeInternalCoordinator() {
	internalCoord.mu.Lock()
	c := internalCoord.c
	internalCoord.c = nil
	internalCoord.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

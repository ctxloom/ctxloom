package spawn

import (
	"context"
	"errors"

	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Runtimes is the runner-start port (docker | podman | host). Start PREPARES
// and ATTACHES (the runner process is running and its handle is live), then
// returns. The ctx it receives has exactly the meaning StartRunner documents.
type Runtimes interface {
	Start(ctx context.Context, l launch.Launch, env map[string]string) (coord.RunnerHandle, error)
}

var errUnimplemented = errors.New("spawn: not implemented")

// StartRunner starts the runner for a resolved launch. The ctx scopes
// PREPARATION AND ATTACH ONLY: a cancellation before attach aborts the
// preparation and removes what it created (no orphaned container, worktree
// or pty), and StartRunner returns ctx.Err(). At attach, ownership of the
// running runner transfers to the run record: a cancellation after attach is
// IGNORED, and the ctx is never the teardown handle. Teardown has one door —
// agent_stop, terminateRun, the idle reaper, or the runner's own exit — and
// a single cancelled call never tears down a running container.
func StartRunner(ctx context.Context, rt Runtimes, l launch.Launch, reach sessions.Endpoint) (coord.RunnerHandle, error) {
	return coord.RunnerHandle{}, errUnimplemented
}

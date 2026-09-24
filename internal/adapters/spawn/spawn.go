package spawn

import (
	"context"
	"os"

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

// StartRunner starts the runner for a resolved launch. The ctx scopes
// PREPARATION AND ATTACH ONLY: a cancellation before attach aborts the
// preparation and removes what it created (no orphaned container, worktree
// or pty), and StartRunner returns ctx.Err(). At attach, ownership of the
// running runner transfers to the run record: a cancellation after attach is
// IGNORED, and the ctx is never the teardown handle. Teardown has one door —
// agent_stop, terminateRun, the idle reaper, or the runner's own exit — and
// a single cancelled call never tears down a running container.
//
// The runner's env is the reach-back trio plus the operator's owner-loss
// window override when one is set: the runner reads and validates it, and a
// container runner would otherwise never see it.
func StartRunner(ctx context.Context, rt Runtimes, l launch.Launch, reach sessions.Endpoint) (coord.RunnerHandle, error) {
	env := sessions.EncodeReach(reach, l.Identity.RunID)
	if v, ok := os.LookupEnv(sessions.EnvRunnerOwnerLossWindow); ok {
		env[sessions.EnvRunnerOwnerLossWindow] = v
	}
	return rt.Start(ctx, l, env)
}

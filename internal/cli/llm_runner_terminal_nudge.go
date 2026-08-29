package cli

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// terminalNudgeBackend decorates an interactive backend so its OWN stdin
// becomes coord.TerminalInjector's delivery seam. It exists only for a Home
// with no EngineHost (llm_serve.go wires it in exactly there): that Home's
// deliverNotice has no turn sink to hand an arrival to, and this is what
// gives it a terminal to inject into instead. A backend driven via
// agent.StructuredChat's Chat method (every EngineHost-backed run) never
// calls Execute at all, so wrapping Execute here cannot double-deliver to a
// target that already has a turn sink — the two paths are disjoint by
// construction, not by a check added here.
type terminalNudgeBackend struct {
	agent.Backend
	home *coord.Home
}

// withTerminalNudge wraps backend for home, or returns backend unchanged when
// home is nil (no coordinator reach-back at all — nothing to nudge).
func withTerminalNudge(backend agent.Backend, home *coord.Home) agent.Backend {
	if home == nil {
		return backend
	}
	return &terminalNudgeBackend{Backend: backend, home: home}
}

// Execute wraps only the interactive, stdin-carrying call shape (ExecuteCLI's
// own ModeInteractive branch): a oneshot run's Stdin is nil here by contract
// (agent.LaunchBackend.ExecuteCLI's doc comment), and there is no terminal to
// inject into.
func (b *terminalNudgeBackend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	if req.Mode != agent.ModeInteractive || req.Stdin == nil {
		return b.Backend.Execute(ctx, req, stdout, stderr)
	}
	injector := coord.NewTerminalInjector(b.home)
	stdin, wrappedStdout := injector.Wrap(req.Stdin, stdout)
	reqCopy := *req
	reqCopy.Stdin = stdin
	return b.Backend.Execute(ctx, &reqCopy, wrappedStdout, stderr)
}

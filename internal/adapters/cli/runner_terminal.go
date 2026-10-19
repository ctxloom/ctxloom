package cli

import (
	"context"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// stdioTerminal is the runner's Terminal over the process's own stdio: the
// pty slave the originator holds the master of (adapters/hostpty), or the
// container's -it tty. The engine is driven through the backend's
// interactive Execute ON that terminal — it inherits the stdio as its own,
// so the runner neither reads the terminal nor relays a byte or a resize —
// over the projection and the presentations the runner delivered, never a
// Setup of its own. Nothing is ever typed into the engine on the human's
// behalf: coordinator mail reaches the session owner through its turn-start
// hook.
type stdioTerminal struct {
	backend  agent.Backend
	terminal *agent.Terminal
	// stdout is where an engine that runs in this process writes (the
	// in-process mock); a launched engine writes to the terminal itself.
	stdout io.Writer
	stderr io.Writer
}

// Run implements runner.Terminal.
func (t stdioTerminal) Run(ctx context.Context, turn runner.Turn) (int, error) {
	session := turn.Launch.Session()
	for _, srv := range turn.MCPServers {
		session.MCPServers = append(session.MCPServers, srv.Name)
	}
	var prompt *agent.Fragment
	if turn.Prompt != "" {
		prompt = &agent.Fragment{Content: turn.Prompt}
	}
	req := &agent.ExecuteRequest{
		Prompt:    prompt,
		WorkDir:   turn.Launch.Cell.Paths.Paths().ProjectRoot.Engine,
		Mode:      agent.ModeInteractive,
		Model:     turn.Launch.Label.Model,
		Env:       turn.EngineEnv(),
		Terminal:  t.terminal,
		Session:   &session,
		Presented: turn.Presented,
	}
	if w, ok := t.backend.(interface{ SetWorkDir(string) }); ok {
		w.SetWorkDir(req.WorkDir)
	}
	result, err := t.backend.Execute(ctx, req, t.stdout, t.stderr)
	if cerr := t.backend.Cleanup(ctx); cerr != nil {
		clidiag.Warn("ctxloom", "backend cleanup failed: %v", cerr)
	}
	if err != nil {
		return 1, err
	}
	if result == nil {
		return 0, nil
	}
	return int(result.ExitCode), nil
}

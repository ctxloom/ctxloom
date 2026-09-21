package cli

import (
	"context"
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// stdioTerminal is the runner's Terminal over the process's own stdio: the
// pty slave the originator holds the master of (adapters/hostpty), or the
// container's -it tty. The engine is driven through the backend's
// interactive Execute — its tmux pane, with the runner's stdin typed into
// it and the pane's bytes relayed to the runner's stdout — over the
// projection and the presentations the runner delivered, never a Setup of
// its own. The injector wraps the pair so coordinator mail is typed into
// the pane between the human's keystrokes (runner.TerminalInjector).
type stdioTerminal struct {
	backend  agent.Backend
	injector *runner.TerminalInjector
	stdin    *os.File
	stdout   io.Writer
	stderr   io.Writer
}

// Run implements runner.Terminal.
func (t stdioTerminal) Run(ctx context.Context, turn runner.Turn) (int, error) {
	var stdin io.Reader = t.stdin
	stdout := t.stdout
	if t.injector != nil {
		var release func()
		stdin, stdout, release = t.injector.Wrap(stdin, stdout)
		defer release()
	}
	session := turn.Launch.Session()
	for _, srv := range turn.MCPServers {
		session.MCPServers = append(session.MCPServers, srv.Name)
	}
	var prompt *agent.Fragment
	if turn.Prompt != "" {
		prompt = &agent.Fragment{Content: turn.Prompt}
	}
	req := &agent.ExecuteRequest{
		Prompt:      prompt,
		WorkDir:     turn.Launch.Cell.Workspace,
		Mode:        agent.ModeInteractive,
		Model:       turn.Launch.Label.Model,
		Env:         turn.Launch.EngineEnv(),
		Permissions: turn.Launch.Permission,
		CellKind:    coordgrpc.CellKindOf(turn.Launch.Cell),
		Stdin:       stdin,
		Resize:      turnResize(ctx, t.stdin),
		Session:     &session,
		Presented:   turn.Presented,
	}
	// The process's stdin is the terminal, owned by this process for its
	// whole life: no StdinCleanup — nothing downstream may close it.
	if w, ok := t.backend.(interface{ SetWorkDir(string) }); ok {
		w.SetWorkDir(req.WorkDir)
	}
	result, err := t.backend.Execute(ctx, req, stdout, t.stderr)
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

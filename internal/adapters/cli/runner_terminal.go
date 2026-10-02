package cli

import (
	"context"
	"io"
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// stdioTerminal is the runner's Terminal over the process's own stdio: the
// pty slave the originator holds the master of (adapters/hostpty), or the
// container's -it tty. The engine is driven through the backend's
// interactive Execute — on its own pty, with the runner's stdin copied into
// it and its output relayed to the runner's stdout — over the projection
// and the presentations the runner delivered, never a Setup of its own.
// Nothing is ever typed into the engine on the human's behalf: coordinator
// mail reaches the session owner through its turn-start hook.
type stdioTerminal struct {
	backend agent.Backend
	stdin   *os.File
	stdout  io.Writer
	stderr  io.Writer
}

// Run implements runner.Terminal.
func (t stdioTerminal) Run(ctx context.Context, turn runner.Turn) (int, error) {
	var stdin io.Reader = t.stdin
	stdout := t.stdout
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

// turnResize adapts the cross-platform watchResize (SIGWINCH-sourced
// agent.WindowSize on unix; the single ConPTY size on Windows) to the engine's
// agent.WindowSize channel, closing when watchResize closes (ctx done). On
// the runner's tty every resize the originator applies to the pty master —
// or the daemon raises on a container's tty — lands here as a SIGWINCH.
func turnResize(ctx context.Context, f *os.File) <-chan agent.WindowSize {
	src := watchResize(ctx, f)
	out := make(chan agent.WindowSize, 1)
	go func() {
		defer close(out)
		for ws := range src {
			s := *ws
			select {
			case out <- s:
			default:
				// Latest-wins: evict the stale pending size, then send the newer.
				select {
				case <-out:
				default:
				}
				out <- s
			}
		}
	}()
	return out
}

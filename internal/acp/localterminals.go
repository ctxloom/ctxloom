package acp

import (
	"context"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// localTerminals is the ACP terminal/* face over tmux hosting.
//
// The hosting itself lives in internal/tmuxhost and knows nothing about ACP.
// This type is the translation layer between the SDK's wire structs and that
// package's own vocabulary, and it exists because the two were previously the
// SAME thing: tmux hosting was written with SDK request/response structs AS
// its method signatures, which bound a general capability to one consumer.
//
// The translation below is mechanical and slightly tedious, and that is the
// intended shape rather than a wart to optimise away. tmuxhost's API is
// deliberately NOT modelled on terminal/*, because this face is scheduled for
// deletion with the rest of this package and an API bent to fit a dying
// consumer would outlive it. The awkwardness belongs on this side of the line.
type localTerminals struct{ *tmuxhost.Terminals }

func newLocalTerminals(runner tmuxhost.Runner, tmpDir string) *localTerminals {
	return &localTerminals{Terminals: tmuxhost.New(runner, tmpDir)}
}

func (l *localTerminals) create(ctx context.Context, req api.CreateTerminalRequest) (api.CreateTerminalResponse, error) {
	spec := tmuxhost.Spec{
		Command:     req.Command,
		Args:        req.Args,
		OutputLimit: req.OutputByteLimit,
	}
	// Cwd crosses as a plain string: the SDK distinguishes "absent" from
	// "empty", and tmuxhost does not, because both mean the same thing to
	// `tmux new-window` — inherit. Collapsing them here keeps that judgement
	// on the ACP side of the line.
	if req.Cwd != nil {
		spec.Cwd = *req.Cwd
	}
	for _, e := range req.Env {
		spec.Env = append(spec.Env, tmuxhost.EnvVar{Name: e.Name, Value: e.Value})
	}
	id, err := l.Create(ctx, spec)
	if err != nil {
		return api.CreateTerminalResponse{}, err
	}
	return api.CreateTerminalResponse{TerminalId: api.TerminalId(id)}, nil
}

func (l *localTerminals) output(_ context.Context, req api.TerminalOutputRequest) (api.TerminalOutputResponse, error) {
	out, err := l.Output(tmuxhost.TerminalID(req.TerminalId))
	if err != nil {
		return api.TerminalOutputResponse{}, err
	}
	return api.TerminalOutputResponse{
		Output:     out.Text,
		Truncated:  out.Truncated,
		ExitStatus: exitStatusToAPI(out.Exit),
	}, nil
}

func (l *localTerminals) wait(ctx context.Context, req api.WaitForTerminalExitRequest) (api.WaitForTerminalExitResponse, error) {
	st, err := l.Wait(ctx, tmuxhost.TerminalID(req.TerminalId))
	if err != nil {
		return api.WaitForTerminalExitResponse{}, err
	}
	// A nil status means the command has not finished. The SDK has no way to
	// say that here, so it becomes the zero response — preserving the previous
	// behaviour exactly rather than inventing an exit code for it.
	if st == nil {
		return api.WaitForTerminalExitResponse{}, nil
	}
	return api.WaitForTerminalExitResponse{ExitCode: st.ExitCode, Signal: st.Signal}, nil
}

func (l *localTerminals) kill(ctx context.Context, req api.KillTerminalRequest) (api.KillTerminalResponse, error) {
	if err := l.Kill(ctx, tmuxhost.TerminalID(req.TerminalId)); err != nil {
		return api.KillTerminalResponse{}, err
	}
	return api.KillTerminalResponse{}, nil
}

func (l *localTerminals) release(ctx context.Context, req api.ReleaseTerminalRequest) (api.ReleaseTerminalResponse, error) {
	if err := l.Release(ctx, tmuxhost.TerminalID(req.TerminalId)); err != nil {
		return api.ReleaseTerminalResponse{}, err
	}
	return api.ReleaseTerminalResponse{}, nil
}

// exitStatusToAPI maps tmuxhost's exit status onto the SDK's. nil maps to nil:
// "not finished yet" is a distinct state from any exit, and flattening it to a
// zero-valued status would report a still-running command as a clean exit 0.
func exitStatusToAPI(st *tmuxhost.ExitStatus) *api.TerminalExitStatus {
	if st == nil {
		return nil
	}
	return &api.TerminalExitStatus{ExitCode: st.ExitCode, Signal: st.Signal}
}

package coordgrpc

import (
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	lmgrpc "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// EncodeRunStart projects a resolved launch and its OPENED package onto the
// plugin protocol's run-start message — the go-plugin arm the host's
// interactive run still rides, deleted whole with that arm (Part 4.1, slice
// 13). The assembled context rides as the lead fragment, the prompt, the
// managed surfaces the caller built from the package and the launch's
// exports, and the options — the CELL's workspace as the engine's cwd, the
// floored permission, the mode, the model, the cell kind projected from the
// cell, and the engine env (Launch.EngineEnv). Every launch owns a harp
// and delivers its own surfaces, so the form is always Deliver. verbosity
// is the invoking surface's diagnostic level; it is not a launch fact.
func EncodeRunStart(l launch.Launch, pkg composite.Package, managed *agent.ManagedConfig, verbosity int) *lmgrpc.RunStart {
	env := l.EngineEnv()
	req := &lmgrpc.RunStart{
		Options: &lmgrpc.RunOptions{
			WorkDir:        l.Cell.Workspace,
			PermissionMode: l.Permission.String(),
			Mode:           encodeExecutionMode(l.Mode),
			Env:            env,
			Verbosity:      agent.WireVerbosity(verbosity),
			Model:          l.Label.Model,
			CellKind:       lmgrpc.CellKindToProto(CellKindOf(l.Cell)),
			LaunchForm:     lmgrpc.LaunchFormToProto(agent.LaunchFormDeliver),
		},
	}
	if pkg.Context.Text != "" {
		req.Fragments = []*lmgrpc.Fragment{{Content: pkg.Context.Text}}
	}
	if l.Prompt != "" {
		req.Prompt = &lmgrpc.Fragment{Content: l.Prompt}
	}
	if managed != nil {
		req.ManagedConfig = lmgrpc.ManagedConfigToProto(managed)
	}
	return req
}

// encodeExecutionMode is the mode's plugin-wire value.
func encodeExecutionMode(m engine.Mode) lmgrpc.ExecutionMode {
	if m == engine.Structured {
		return lmgrpc.ExecutionMode_ONESHOT
	}
	return lmgrpc.ExecutionMode_INTERACTIVE
}

// CellKindOf projects the cell onto today's writers' cell kind: a container
// is process-isolated, a workspace apart from the project root is
// directory-isolated, the project root itself is shared. The runner and the
// plugin arm both read it here, so the two deliver into the same cell kind.
func CellKindOf(c launch.Cell) agent.CellKind {
	switch {
	case c.Container != nil:
		return agent.CellKindProcessIsolated
	case c.Workspace != "" && c.Workspace != c.Paths.Paths().ProjectRoot.Host:
		return agent.CellKindDirectoryIsolated
	}
	return agent.CellKindShared
}

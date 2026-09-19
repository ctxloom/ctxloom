// Package coordgrpc is the wire codec's home: the one place a resolved
// launch is projected onto a message. EncodeLaunch targets the plugin
// protocol's run-start message as it stands; the coordination proto's own
// Launch message replaces that target when the wire changes, and the field
// set test is what moves with it.
package coordgrpc

import (
	"maps"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
)

// EncodeLaunch projects a resolved launch onto the run-start message: the
// assembled context as the lead fragment, the prompt, the managed surfaces,
// and the options — the CELL's workspace as the engine's cwd, the floored
// permission, the mode, the model, the cell kind projected from the cell,
// and the env: the identity carriers stamped from the identity, the cell's
// own env beneath the caller's passthrough. Every launch owns a harp and
// delivers its own surfaces, so the form is always Deliver. verbosity is the
// invoking surface's diagnostic level; it is not a launch fact.
func EncodeLaunch(l launch.Launch, verbosity int) *pb.RunStart {
	env := map[string]string{}
	maps.Copy(env, l.Cell.Env)
	maps.Copy(env, l.Env)
	maps.Copy(env, sessions.HookEnv(l.Identity))

	req := &pb.RunStart{
		Options: &pb.RunOptions{
			WorkDir:        l.Cell.Workspace,
			PermissionMode: l.Permission.String(),
			Mode:           encodeMode(l.Mode),
			Env:            env,
			Verbosity:      agent.WireVerbosity(verbosity),
			Model:          l.Label.Model,
			CellKind:       pb.CellKindToProto(cellKindOf(l.Cell)),
			LaunchForm:     pb.LaunchFormToProto(agent.LaunchFormDeliver),
		},
	}
	if l.Package.Context != "" {
		req.Fragments = []*pb.Fragment{{Content: l.Package.Context}}
	}
	if l.Prompt != "" {
		req.Prompt = &pb.Fragment{Content: l.Prompt}
	}
	if managed, ok := l.Package.Managed.(*agent.ManagedConfig); ok && managed != nil {
		req.ManagedConfig = pb.ManagedConfigToProto(managed)
	}
	return req
}

// encodeMode is the mode's wire value.
func encodeMode(m engine.Mode) pb.ExecutionMode {
	if m == engine.Structured {
		return pb.ExecutionMode_ONESHOT
	}
	return pb.ExecutionMode_INTERACTIVE
}

// cellKindOf projects the cell onto the wire's cell kind: a container is
// process-isolated, a workspace apart from the project root is
// directory-isolated, the project root itself is shared.
func cellKindOf(c launch.Cell) agent.CellKind {
	switch {
	case c.Container != nil:
		return agent.CellKindProcessIsolated
	case c.Workspace != "" && c.Workspace != c.Paths.Paths().ProjectRoot.Host:
		return agent.CellKindDirectoryIsolated
	}
	return agent.CellKindShared
}

package cli

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
)

// resolvedLaunch is what today's run resolution decides before the wire
// payload is built: the engine and label the launch source resolved to, the
// label's model, the floored permission posture, and the isolation axes.
type resolvedLaunch struct {
	Engine     string
	Label      string
	Model      string
	Permission engine.PermissionMode
	Axes       launch.Axes
}

// resolveViaPhases resolves a launch.Source through the phases `run` runs
// today — the launch-source arm the source selects, then the workspace, mode
// and posture steps — on a run state of its own. It computes exactly what
// runRun computes for the same inputs; the five-source parity table holds
// the two equal. It is the seam the one resolver replaces: every caller that
// asks for a launch is a Source, and this is where the Source meets today's
// phases until launch.Resolve exists.
//
// The source's fields map onto the flags the arms take: Agent is --agent,
// the first of Profiles is -p (today's flag names one profile), Label the
// --llm override, Mode the --one-shot choice, Workspace the --workspace flag
// and Permission the --permissions flag. A zero Permission is "not
// requested" and reaches the posture chain as the empty flag, exactly as an
// omitted flag does.
func resolveViaPhases(ctx context.Context, cfg *config.Config, src launch.Source) (resolvedLaunch, error) {
	st := &runState{ctx: ctx, cfg: cfg}

	runtime, err := launch.ParseRuntimeAxis(cfg.GetRuntime())
	if err != nil {
		return resolvedLaunch{}, err
	}
	st.agentRuntime = runtime

	switch {
	case src.Agent != "":
		err = st.resolveNamedAgent(src.Agent, src.Label)
	case len(src.Profiles) == 0:
		err = st.resolveDefaultAgent(src.Label)
	default:
		err = st.resolveClassicAssembly(src.Profiles[0], nil, nil, src.Label)
	}
	if err != nil {
		return resolvedLaunch{}, err
	}

	st.resolveSessionWorkspace(string(src.Workspace))
	st.resolveMode(src.Mode == engine.Structured)
	flag := ""
	if src.Permission != 0 {
		flag = src.Permission.String()
	}
	if err := st.resolvePostureAndAxes(flag); err != nil {
		return resolvedLaunch{}, err
	}
	return resolvedLaunch{
		Engine:     st.backendName,
		Label:      st.label,
		Model:      st.labelModel,
		Permission: st.permMode,
		Axes:       st.runAxes,
	}, nil
}

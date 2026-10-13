package kit

import (
	"context"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// BackendDefaults are what an engine's projected backend needs beyond its
// kind: the engine supplies them as values.
type BackendDefaults struct {
	// Engine names the backend (agent.Backend's Name) and the request
	// session's label.
	Engine string
	// Provider is the ModelInfo provider a run reports.
	Provider string
	// HomeVar is the env var a request names the engine home by, bound as
	// the session's home when the request carries no session.
	HomeVar string
	// Policy is the permission policy of a session projected from a request
	// that carries none.
	Policy func() engine.PermissionPolicy
}

// ProjectedBackend is agent.Backend as a projection onto the engine kind:
// every argv it runs is kind.Instance(session).Exec(presented), over the
// shared launch core (agent.LaunchBackend). An engine's Hosted.Backend is
// one NewProjectedBackend call.
type ProjectedBackend struct {
	agent.LaunchBackend
	kind engine.Engine
	d    BackendDefaults
}

// NewProjectedBackend is the backend projecting requests onto kind, over
// launch (nil leaves the launcher unset, for SetLauncher later).
func NewProjectedBackend(kind engine.Engine, launch agent.Launcher, d BackendDefaults) *ProjectedBackend {
	b := &ProjectedBackend{kind: kind, d: d}
	b.BaseBackend = agent.NewBaseBackend(d.Engine, "1.0.0")
	b.InitLaunch(agent.NewBaseContextProvider())
	// The home var names the engine home a request's run env advises; a run
	// without one advises no engine home.
	b.SetEngineHomeVar(d.HomeVar)
	if launch != nil {
		b.SetLauncher(launch)
	}
	return b
}

// Kind is the engine kind the backend projects onto.
func (b *ProjectedBackend) Kind() engine.Engine { return b.kind }

// SessionFor is the engine-facing Session the instance is bound to: the
// runner's projection when the request carries one, else one projected from
// the request — the harp the run env carries, the model, the mode, the
// engine's default policy, the prompt, the working directory and the home the
// run env names. Every production request carries the runner's session (its
// terminal builds the only one); the projection serves request-driven tests,
// and names no binary or args, so its Exec runs the engine's own CLI bare.
func (b *ProjectedBackend) SessionFor(req *agent.ExecuteRequest) engine.Session {
	if req.Session != nil {
		return *req.Session
	}
	s := engine.Session{
		Identity:   sessions.Identity{Harp: req.Env[agent.SessionHarpEnv]},
		Label:      engine.LabelConfig{Label: b.d.Engine, Model: req.Model},
		Mode:       req.Mode,
		Permission: b.d.Policy(),
		Prompt:     agent.GetPromptContent(req.Prompt),
		WorkDir:    b.WorkDir(),
	}
	if home := req.Env[b.d.HomeVar]; home != "" {
		s.Home = []engine.HomeBinding{{Var: b.d.HomeVar, Path: home}}
	}
	return s
}

// ExecFor is the request's Exec: the instance bound to the request's
// session, over what the runner delivered (req.Presented).
func (b *ProjectedBackend) ExecFor(req *agent.ExecuteRequest) (engine.Exec, error) {
	inst, err := b.kind.Instance(b.SessionFor(req))
	if err != nil {
		return engine.Exec{}, err
	}
	return inst.Exec(req.Presented)
}

// Execute runs the request's Exec — its binary over its args, so what is
// spawned is what the engine composed: an interactive one on the launcher's pty
// with the frontend's stdin, a one-shot with its task on stdin (PromptStdin).
func (b *ProjectedBackend) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Best-effort model identity: the requested model, else the backend's
	// name rather than a fabricated version.
	modelName := req.Model
	if modelName == "" {
		modelName = b.Name()
	}
	ex, err := b.ExecFor(req)
	if err != nil {
		return nil, err
	}
	defer ReleaseSocketRoot(b.SessionFor(req))
	b.SetExecuteEnv(func(*agent.ExecuteRequest) map[string]string { return ex.Env })
	var oneshotStdin io.Reader
	if req.Mode == agent.ModeOneshot {
		oneshotStdin = PromptStdin(req)
	}
	return b.ExecuteCLI(ctx, req, ex.Binary, ex.Args, oneshotStdin, &agent.ModelInfo{ModelName: modelName, Provider: b.d.Provider}, stdout, stderr)
}

// PromptStdin is the one-shot task as a stdin reader, or nil when there is
// no prompt: on stdin, a large task cannot exceed the OS argv limit (E2BIG).
func PromptStdin(req *agent.ExecuteRequest) io.Reader {
	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		return strings.NewReader(prompt)
	}
	return nil
}

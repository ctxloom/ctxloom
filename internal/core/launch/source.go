package launch

import (
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Source is what a caller KNOWS when it asks for a launch — never more. Every
// way a launch is asked for is a Source value through one Resolve.
type Source struct {
	Identity sessions.Identity // REQUIRED: minted by the caller (operations.StartRun, coord.AgentRun); Resolve refuses a zero value
	// Orchestrator is the harp of the ROOT session this launch runs under —
	// the coordinator's own engine, the single ctxloom-side refresher whose
	// credential every agent holds a read-only projection of. Empty for the
	// root itself (Identity.Depth == 0: it is its own orchestrator and seeds
	// whole, two-way with the host); REQUIRED for an agent (Depth > 0),
	// which has nothing to project from otherwise (ErrNoOrchestrator).
	Orchestrator string
	Agent        string
	Profiles     []string
	// Fragments and Tags are the explicit-assembly arm's selection beyond
	// the profile set (`run -f`, `run -t`): named fragments and tag matches
	// composed with the profiles. Only the profile-set arm reads them.
	Fragments  []string
	Tags       []string
	Label      string
	Model      string // overrides the label's model for this launch; empty keeps the label's
	Mode       engine.Mode
	Prompt     string
	WorkDir    string
	Workspace  WorkspaceAxis
	DirtyTree  DirtyTreeHandler
	Permission engine.PermissionMode // the flag; zero = not requested
	Resume     Resume
	Degraded   bool
	// Env is the caller's engine passthrough (`run --env`); the identity
	// carriers are stamped by Resolve and never taken from here.
	Env map[string]string
	// Extra are the context blocks the caller composes at launch time beyond
	// the selection — a resumed session's transcript, this launch's startup
	// findings — appended to the package's context after the assembly, in
	// order (composite.Package.WithLead).
	Extra []composite.Fragment
	// Internal marks an internal one-shot (a distill, a triage, the setup
	// probe): no binding and no profiles are selected — the prompt is the
	// whole instruction and Label names the engine. It is still a real
	// session: a harp, an endpoint, the managed surfaces. It exists because
	// no shipped binding names these runs; a binding for each retires it.
	Internal bool
}

// Resume is the resume arm. A non-zero Ref makes Resolve REUSE the session:
// the same harp, the recorded MCP endpoint (unless RebindEndpoint), the
// native key. It is how the coordinator's recovery re-resolves a journaled
// run after a restart (Source{Agent: rec.Agent, Identity: id, Resume: …}).
type Resume struct {
	Ref            sessions.ResumeRef
	RebindEndpoint bool
}

package launch

import (
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// Source is what a caller KNOWS when it asks for a launch — never more. Every
// way a launch is asked for is a Source value through one Resolve.
type Source struct {
	Identity   sessions.Identity // REQUIRED: minted by the caller (operations.StartRun, coord.AgentRun); Resolve refuses a zero value
	Agent      string
	Profiles   []string
	Label      string
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
}

// Resume is the resume arm. A non-zero Ref makes Resolve REUSE the session:
// the same harp, the recorded MCP endpoint (unless RebindEndpoint), the
// native key. It is how the coordinator's recovery re-resolves a journaled
// run after a restart (Source{Agent: rec.Agent, Identity: id, Resume: …}).
type Resume struct {
	Ref            sessions.ResumeRef
	RebindEndpoint bool
}

package mcp

import (
	"errors"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// ctxServer holds shared state used by every host-relayed tool handler. The
// coordinator's relay (HostApp.Serve) builds one per call, its identity the
// CALLER's credential-derived identity — so a relayed tool sees the caller's
// session, never the host process's env.
type ctxServer struct {
	// cfg is the generation this server serves — the one the coordinator was
	// composed over.
	cfg *config.Config
	// self is the caller identity every identity-consuming tool uses: from
	// the credential on the coordinator's surface.
	self coord.Identity
	// hosts yields the coordinator an internal one-shot this server starts
	// (a triage) runs on: the session's own, on the coordinator's
	// relay (HostApp). Nil refuses the one-shot (operations.ErrNoRunHost).
	hosts operations.RunHosts
	// facts is what every launch and by-name engine resolution a handler
	// makes reads: the hosting App's registry and claim store, under the
	// strict posture, handed through HostApp.
	facts operations.LaunchFacts
}

// hostsFor yields the one-shot host port for s; a nil port is the refusal
// StartOneShot names.
func (s *ctxServer) hostsFor() operations.RunHosts { return s.hosts }

// errNoCallerProject refuses a relayed call whose identity names no project:
// a handler answers for the CALLER's cell, and there is no cwd to fall back
// on in the coordinator's process — reading one would answer for the host,
// not the caller.
var errNoCallerProject = errors.New("mcp: the caller's identity names no project to answer for")

// projectDir is the project a relayed handler answers for: the caller's OWN
// identity, which the credential the coordinator issued carries. It differs
// from the serving process's cwd exactly where it matters — a host-relayed
// tool runs in the coordinator's process on behalf of a caller whose cell is
// another project, or a per-agent worktree — so the identity is the only
// source. Every handler that needs the project reads it here; none consults
// the environment.
func (s *ctxServer) projectDir() (string, error) {
	if s.self.ProjectDir == "" {
		return "", errNoCallerProject
	}
	return s.self.ProjectDir, nil
}

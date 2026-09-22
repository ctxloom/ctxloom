package mcp

import (
	"errors"

	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	// distill collapses concurrent distillations of the SAME session into one
	// run. It is SHARED across ctxServer instances (the coordinator builds a
	// fresh one per relayed call), so it is injected, never owned here. Nil
	// disables the dedupe — the work still happens, just undeduped.
	distill *singleflight.Group
	// compactorFactory builds the compactor distillSessionOnce runs. Nil means
	// the real one, which is every production path; a test substitutes a
	// mock-backed one. It exists because distillSessionOnce's post-distill
	// behaviour — above all WHICH KEY it reads the fresh essence back under —
	// was otherwise unreachable without a live LLM, and a mutation swapping that
	// key survived the entire package unnoticed.
	compactorFactory func(memory.CompactionConfig) (*memory.Compactor, error)
	// hosts yields the coordinator an internal one-shot this server starts
	// (a distill, a triage) runs on: the session's own, on the coordinator's
	// relay (HostApp). Nil refuses the one-shot (operations.ErrNoRunHost).
	hosts operations.RunHosts
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
	if s.self.Project == "" {
		return "", errNoCallerProject
	}
	return s.self.Project, nil
}

// strictness is the posture a relayed handler reports under: the relay runs
// in the coordinator's process with no composition of its own to read one
// from, so it is strict.
func (s *ctxServer) strictness() strictness.Mode { return strictness.Mode{Prog: "ctxloom"} }

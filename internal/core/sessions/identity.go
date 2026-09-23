package sessions

import "github.com/ctxloom/ctxloom/internal/shared/harp"

// Identity is the session identity: what a credential authenticates AND
// identifies. It derives from the credential/connection per request — never
// from the calling process's env (the shared HTTP server must not see the
// host process env as caller identity) — and is carried typed on every
// coordination verb. Never re-read from env or cwd inside a process that
// already holds one.
type Identity struct {
	// Harp is the caller's session harp (a child's harp, or the parent
	// session's own harp for the session-owner credential); it names the
	// session dir and the spool.
	Harp string `json:"harp"`
	// RunID is the spawned run this credential was minted for; empty on
	// session-owner (parent) credentials. Stamped once by WithRun.
	RunID string `json:"run_id,omitempty"`
	// Depth is the delegation depth: 0 = the originator's own run; children
	// are deeper. The CAP is config (delegation.depth); nothing here says how
	// many depths exist. The recursion guard derives from THIS, not from env.
	Depth int `json:"depth"`
	// OneShot mirrors THIS run's own resume mode being one-shot: its engine
	// tears down and is resumed by native session key at every turn
	// boundary, so it cannot hold a coordination relationship across turns.
	// A one-shot run is a leaf regardless of Depth.
	OneShot bool `json:"one_shot,omitempty"`
	// Leaf is the coordinator's verdict of IsLeaf at mint — a one-shot run,
	// or one at the delegation-depth cap — stamped so the runner, which
	// holds no config to read the cap from, withholds the coordinator-only
	// tools by the same rule the coordinator refuses agent_run by.
	Leaf bool `json:"leaf,omitempty"`
	// Project is the resolved project id the coordinator serves — a single
	// clean path segment (tasks/paths.ValidateProjectID), exported to the
	// engine as EnvProjectID. Never a directory. Empty when the id did not
	// resolve.
	Project string `json:"project,omitempty"`
	// ProjectDir is the project directory the coordinator serves: what a
	// host-relayed handler answers for. Coordinator-side only — stamped when
	// the coordinator identifies a caller, never on the wire (a runner has
	// its own cwd).
	ProjectDir string `json:"-"`
	// Consumer marks a read-only watch credential: it authenticates the
	// consumer plane ONLY — the coordinator's auth interceptor rejects it on
	// every coordination method, so a leaked viewer credential cannot mutate
	// anything or impersonate a runner/child. Never journaled: minted fresh
	// per coordinator process, verified in-memory only. It is the
	// coordinator's concern and rides on this value only because the
	// coordinator identifies every caller through the one Identity.
	Consumer bool `json:"-"`
}

// IsChild reports whether this identity belongs to a spawned child run.
func (id Identity) IsChild() bool { return id.Depth > 0 }

// IsLeaf is the one leafness rule: a one-shot run, or a run at or past the
// delegation-depth cap, may not delegate.
func (id Identity) IsLeaf(cap int) bool { return id.OneShot || id.Depth >= cap }

// WithRun returns the identity stamped with the runtime coordinator's run id.
func (id Identity) WithRun(runID string) Identity { id.RunID = runID; return id }

// Validate applies the harp rule: an identity with no valid harp names no
// session.
func (id Identity) Validate() error { return harp.Validate(id.Harp) }

// Endpoint is one addressable ctxloom service: the runtime coordinator a
// runner reaches back to, or the session's MCP endpoint an engine addresses.
// Minted by whoever OWNS the address and handed typed to whoever must dial or
// bind it. Never discovered.
type Endpoint struct {
	URL        string
	Credential string
}

// ResumeRef names a session to continue and the engine's native key for it.
// A zero NativeKey with a non-zero Harp means "same harp, fresh engine".
type ResumeRef struct {
	Harp      string
	NativeKey string
}

// Seed is what a mint needs to know. Engine is a string here on purpose:
// sessions never imports engine.
type Seed struct {
	ProjectDir string
	ProjectID  string
	Engine     string
	Depth      int
	OneShot    bool
}

// Origin is who a session was minted for, recorded at the mint so a later
// process can tell a human's session from ctxloom's own internal work.
type Origin string

const (
	// OriginSession is a human's session: a run at depth 0 that is not a
	// one-shot. Its transcript is never destroyed undistilled by a sweep.
	OriginSession Origin = "session"
	// OriginAgent is a delegated child's session.
	OriginAgent Origin = "agent"
	// OriginOneShot is an internal one-shot (a distiller, a probe): a single
	// machine-driven turn nobody resumes, so a sweep may purge it without an
	// essence.
	OriginOneShot Origin = "oneshot"
)

// Origin is the origin this seed mints. One-shot-ness wins over depth: a
// one-shot spawned below a coordinator is still internal work.
func (s Seed) Origin() Origin {
	switch {
	case s.OneShot:
		return OriginOneShot
	case s.Depth > 0:
		return OriginAgent
	default:
		return OriginSession
	}
}

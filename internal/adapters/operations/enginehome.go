package operations

import (
	"fmt"
	"os"
	"path"

	"github.com/ctxloom/ctxloom/internal/adapters/agents"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// InTreeAgentHome is one run's claim on a ctxloom-controlled engine config
// home: everything ResolveInTreeAgentHome needs to decide whether this run
// gets one, where its bytes live, and where the engine is told they are.
//
// There is deliberately NO isolation policy or workspace here. Whether a run
// HAS a controlled home is decided by HomeMode alone; the cell it runs in
// (host or container, live tree or worktree) decides only how the home is
// PRESENTED — that is Runtime's job, and it can only move the Engine side of
// the root, never make the home go away.
type InTreeAgentHome struct {
	// Backend is the resolved backend name (internal/lm/backends).
	Backend string
	// Cwd is the directory the engine actually RUNS in — the prepared
	// workspace's Dir(): the project root itself on the live tree, a checkout
	// elsewhere on a worktree cell. It is what the seeded instance config's
	// workspace-trust answer names, because trusting WorkDir would answer for
	// a directory a worktree run never enters.
	Cwd string
	// Harp is THIS SESSION's name, the instance key (paths.HarpSessionHome:
	// ~/.ctxloom/sessions/<Harp>/home). Empty resolves ABSENT — no
	// env var, no directory, a warn — because there is no session-less
	// instance, and falling back to a project-wide path would recreate the
	// durable per-project engine home the per-session model retired.
	Harp string
	// HomeMode is the run's resolved agent binding's EFFECTIVE config-home
	// policy — agents.HomeModeSession or agents.HomeModeHost when a
	// binding was resolved (operations.ResolvedAgent.HomeMode, already
	// defaulted), or "" when this run has NO agent binding at all. It is THE
	// scoping condition; see ResolveInTreeAgentHome's doc. Only
	// agents.HomeModeSession gets a home — "" (no binding) and
	// agents.HomeModeHost (an undeclared or explicitly host-declared
	// binding) both mean "keep the home the runtime gives you".
	HomeMode agents.HomeMode
	// ContainerHome is the runtime axis's half: the FIXED in-container root a
	// relocated home is mounted under when the engine runs in a container
	// (isolation.ContainerInstanceHome), or "" when it runs on the host and
	// opens the host path itself. Non-empty is what makes the resolution
	// carry a Mount — Root.Host bound at <ContainerHome>/<the engine's
	// declared leaf>. It decides WHERE the engine is told the home is, never
	// WHETHER there is one.
	ContainerHome string
}

// AgentHomeResolution is what one run learned about its controlled engine
// home. Exactly one of two shapes: PRESENT (Root names the home on both sides,
// Env tells the engine, Mount is non-nil exactly when Engine differs from
// Host) or ABSENT (Root is zero and Absent says why). An empty Root with an
// empty Absent is a bug, never a result: a run that asked for a home and got
// nothing must be able to say what happened.
type AgentHomeResolution struct {
	// Root is the home on both sides: Host is where the bytes live and what a
	// runtime mounts FROM; Engine is what the engine is told.
	Root present.Root
	// Env is the engine's declared home var pointed at Root.Engine.
	Env map[string]string
	// Mount makes Root.Engine true inside a container: Root.Host — this
	// session's instance leaf, the RIGHT host directory — bound at
	// Root.Engine. nil whenever Engine equals Host.
	Mount *present.Mount
	// Absent is "" when the home is present; otherwise WHY this run has none.
	Absent string
	// release stops what preparing the home left running (the seam's
	// InTreeAgentHomeSpec.Prepare). Unexported: the only thing a caller may
	// do with it is Release, at the run's end.
	release func() error
}

// Release stops what preparing the home left RUNNING — the credential
// replicator that keeps the instance in step with the host's rotating token
// for as long as the run lives. It belongs at the run's end (Cells.Prepare
// folds it into the cell's Cleanup): called earlier, the instance's token
// goes stale behind a live engine; never called, every launch leaks a
// watcher into the process that started it. Safe on an absent home and on a
// home that left nothing running.
func (r AgentHomeResolution) Release() error {
	if r.release == nil {
		return nil
	}
	return r.release()
}

// absent is the one constructor for the ABSENT shape, so a reason can never
// be forgotten: the shape is unrepresentable without one.
func absent(format string, args ...any) AgentHomeResolution {
	return AgentHomeResolution{Absent: fmt.Sprintf(format, args...)}
}

// inTreeAgentHomeFixIt is the fix-it on the one finding this file records. It
// names authentication rather than a runtime, and it names --degraded
// truthfully: under --degraded nothing is contributed, so the run falls back
// to the home its runtime gives it.
// It names no flag: the finding is non-degradable (see ResolveInTreeAgentHome),
// because the fallback it used to offer is the SHARED host config home — the
// one thing a per-session engine home exists to avoid. Both routes named here
// end with the private home actually working.
const inTreeAgentHomeFixIt = "authenticate the engine on this host (e.g. `claude login`) or set its API-key env var so the per-session home has credentials to seed, or set `engine_home:` to something other than `session` if this agent should share the runtime's own config home"

// ResolveInTreeAgentHome decides ONE run's controlled engine config home —
// CLAUDE_CONFIG_DIR and its kin pointed at THIS SESSION's ctxloom-controlled
// INSTANCE under paths.HarpSessionHome — and returns it present or absent, with
// the reason when absent. It creates the instance and prepares it as a side
// effect, so a present result always names a directory that exists and (for
// an engine with copyable credentials) can authenticate.
//
// THE SCOPING RULE, and the reason this is not simply "every agent run":
//
//	A run resolved through an agent binding whose EFFECTIVE engine_home is
//	"session" gets a controlled home. Every other run — no binding at all, an
//	undeclared binding, or one that declares "host" — keeps the home its
//	runtime gives it: the REAL host home on the host, a fresh $HOME in a
//	container.
//
// The controlled-home behaviour is strictly OPT-IN: a binding that never
// mentions engine_home resolves to agents.HomeModeHost
// (agents.ParseHomeMode), so declaring the binding at all is not
// enough on its own — an agent that wants its runs kept off the human's real
// ~/.claude must say `engine_home: session`. A delegated child, a fan-out
// member, a `run --agent` — these ARE ctxloom's processes, and pointing an
// unopted one at the human's ~/.claude hands it the human's memory, plugins,
// personal MCP registrations, global agents and steering, and lets it write
// session state and settings edits back into them; that is the pollution
// `engine_home: session` exists to let a binding opt out of. But nothing takes
// that on by default — a project that wants it asks for it by name, on the
// binding that wants it.
//
// The human's own interactive session (no agent binding resolved at all —
// no --agent, no default_agent) has no HomeMode to read in the first
// place and always keeps the real home: relocating a human's own working
// environment out from under their interactive session would be a
// regression dressed as isolation, and there is no binding through which
// they could even opt in.
//
// THE HOME IS ORTHOGONAL TO THE CELL. Nothing here reads which workspace or
// runtime the run chose: a host run, a worktree run and a container run with
// the same binding get the same session instance. The runtime axis
// (in.ContainerHome) rewrites only the ENGINE side of the root — on the host
// the engine is told the host path; in a container it is told
// <ContainerHome>/<leaf>, the leaf being the one the engine DECLARES
// (engine.HomeVar.Subdir, via the backend spec) rather than anything
// re-derived from the host path, and the Mount that makes that true rides
// the resolution for the caller to hand the workspace
// (isolation.MountEngineHome). The workspace axis does not touch the home at
// all: a worktree's own env (isolation.EnvWorkspace) carries scratch and git
// identity, never a config-home var.
//
// THE INSTANCE IS PER SESSION. Two concurrent sessions in one checkout get two
// homes. Two runs WITHIN one session (a coordinator and its delegated child,
// which inherits the harp on req.Env) deliberately share one instance.
//
// ABSENT is never silent about a home that was ASKED for. When HomeMode is
// not "session" the reason is recorded and nothing else happens — that is the
// documented default. When it IS "session" and the run still gets no home
// (no session name, an engine with no relocatable home, an instance that
// cannot be created), the reason is also said out loud, and an engine whose
// credentials cannot be seeded (no API key and no host credential file) is
// FAIL-LOUD: a ClassIsolation finding for the caller's choke gate. Handing the
// engine an empty home it cannot authenticate against would trade a working
// run for a mysterious 401; falling back to the runtime's home is what
// --degraded then actually does.
func ResolveInTreeAgentHome(in InTreeAgentHome) AgentHomeResolution {
	if in.HomeMode != agents.HomeModeSession {
		return absent("engine_home is %q, not %q: the engine keeps the home its runtime gives it", in.HomeMode, agents.HomeModeSession)
	}
	if in.Harp == "" {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: this run carries no session name and a config-home instance is per-session; using the runtime's own config home instead", in.Backend)
		return absent("this run carries no session name and a config-home instance is per-session")
	}
	spec, ok := backends.InTreeAgentHomeFor(in.Backend, in.Harp)
	if !ok {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: engine_home is %q but %s declares no relocatable config home; using the runtime's own config home instead", in.Backend, agents.HomeModeSession, in.Backend)
		return absent("%s declares no relocatable config home", in.Backend)
	}

	home := spec.Dir
	var release func() error
	if spec.Prepare != nil {
		var err error
		release, err = spec.Prepare(in.Cwd)
		if err != nil {
			// NON-DEGRADABLE. The fallback is not "less isolation", it is the
			// SHARED host config home — the agent would read and write the
			// user's real engine credentials and state, which is the precise
			// thing `engine_home: session` was asked for to prevent. Delivery
			// never degrades to a shared home: private is the root, and sharing
			// is only ever something a user selects explicitly.
			strictness.FailAlways(strictness.ClassIsolation, inTreeAgentHomeFixIt,
				"in-tree agent home for %s: %v — refusing to point %s at an unauthenticated %s, and refusing to substitute the SHARED host config home for the per-session one this agent asked for",
				in.Backend, err, spec.EnvVar, home)
			return absent("refusing to point %s at an unauthenticated %s: %v", spec.EnvVar, home, err)
		}
	}
	// Restated after preparation rather than assumed: the copy-in creates the
	// instance only on the path where there WAS something to copy, and an
	// engine with a DECLARED EMPTY ambient set is otherwise handed a variable
	// naming a directory nobody created. 0700 because the tree holds engine
	// config and live credential bytes.
	if err := os.MkdirAll(home, 0o700); err != nil {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: cannot create %s (%v); using the runtime's own config home instead", in.Backend, home, err)
		return AgentHomeResolution{release: release}.releasedAbsent("cannot create %s: %v", home, err)
	}

	var advice present.PathsAdvice = present.Host{}
	if in.ContainerHome != "" {
		// A container path, so joined with forward slashes whatever the
		// host's separator (present.Containerize's own rule for Engine).
		advice = present.Containerize{EngineHome: path.Join(in.ContainerHome, spec.Subdir)}
	}
	paths, mounts := advice.ApplyPaths(present.Paths{EngineHome: present.Root{Host: home}})
	res := AgentHomeResolution{
		Root:    paths.EngineHome,
		Env:     map[string]string{spec.EnvVar: paths.EngineHome.Engine},
		release: release,
	}
	if len(mounts) > 0 {
		m := mounts[0]
		res.Mount = &m
	}
	return res
}

// BindAgentHome is the glue every launch path shares: it reads where the
// prepared workspace's runtime would mount a relocated home, resolves, and —
// for a container — records the mount that makes the engine-side path true.
// The returned resolution's Env is what the caller merges under its run env;
// the caller owns that merge, so a user's own `--env` still wins.
func BindAgentHome(ws isolation.Workspace, in InTreeAgentHome) AgentHomeResolution {
	in.ContainerHome = isolation.ContainerInstanceHome(ws)
	res := ResolveInTreeAgentHome(in)
	if res.Mount == nil {
		return res
	}
	if err := isolation.MountEngineHome(ws, *res.Mount); err != nil {
		strictness.Fail(strictness.ClassIsolation, inTreeAgentHomeFixIt,
			"in-tree agent home for %s: %v; this run uses the runtime's own config home instead", in.Backend, err)
		return res.releasedAbsent("%v", err)
	}
	return res
}

// releasedAbsent is absent for a home that was PREPARED and then could not be
// delivered: what preparing left running is stopped first, because no run is
// going to own it. The reason is reported exactly as absent reports it.
func (r AgentHomeResolution) releasedAbsent(format string, args ...any) AgentHomeResolution {
	if err := r.Release(); err != nil {
		clidiag.Warn("ctxloom", "in-tree agent home: stopping the credential replication of a home this run cannot use: %v", err)
	}
	return absent(format, args...)
}

package operations

import (
	"fmt"
	"os"
	"path"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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
	// Backend is the resolved engine name (the registry key).
	Backend string
	// Cwd is the directory the engine actually RUNS in — the prepared
	// workspace's Dir(): the project root itself on the live tree, a checkout
	// elsewhere on a worktree cell. It is what the seeded instance config's
	// workspace-trust answer names, because trusting WorkDir would answer for
	// a directory a worktree run never enters.
	Cwd string
	// Harp is THIS SESSION's name, the instance key (paths.HarpSessionEngineHomes:
	// ~/.ctxloom/sessions/<Harp>/home). Empty resolves ABSENT — no
	// env var, no directory, a warn — because there is no session-less
	// instance, and falling back to a project-wide path would recreate the
	// durable per-project engine home the per-session model retired.
	Harp string
	// HomeMode is the run's EFFECTIVE engine-home policy —
	// agents.HomeModeSession (the default, whether or not a binding said
	// so) or agents.HomeModeHost (a binding's explicit, unsafe selection).
	// It is THE scoping condition; see ResolveInTreeAgentHome's doc. Only
	// agents.HomeModeSession gets a home; agents.HomeModeHost means "keep
	// the home the runtime gives you". The zero value is nobody's decision
	// and is treated as the default by the parser every caller runs it
	// through (agents.ParseHomeMode).
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
	// Login is the shared login a HOST run authenticates from in place of the
	// token (InTreeAgentHomeSpec.LoginEnv), for the caller to merge beside
	// Env; nil for a container run and for an absent home.
	Login map[string]string
	// Mount makes Root.Engine true inside a container: Root.Host — this
	// session's instance leaf, the RIGHT host directory — bound at
	// Root.Engine. nil whenever Engine equals Host.
	Mount *present.Mount
	// Absent is "" when the home is present; otherwise WHY this run has none.
	Absent string
}

// absent is the one constructor for the ABSENT shape, so a reason can never
// be forgotten: the shape is unrepresentable without one.
func absent(format string, args ...any) AgentHomeResolution {
	return AgentHomeResolution{Absent: fmt.Sprintf(format, args...)}
}

// inTreeAgentHomeRemedy is the fix-it on the one finding this file records.
// The engine-specific remedies — its mint command, its auth vars — ride the
// error the preparation produced (isolation.PrepareInstanceHome); this names
// the shape: a stored or exported token, or the binding's explicit, unsafe
// selection of the real home. It names no
// --degraded: the finding is non-degradable (see ResolveInTreeAgentHome),
// because the only fallback would be the SHARED host home — the one thing
// the session home exists to keep a run off, and a thing only the binding
// may select.
const inTreeAgentHomeRemedy = "store the engine's long-lived token with `ctxloom auth set-token` (or export one of its auth vars), or select the real engine home on the binding with `engine_home: host` — the unsafe selection, never a default"

// ResolveInTreeAgentHome decides ONE run's controlled engine config home —
// CLAUDE_CONFIG_DIR and its kin pointed at THIS SESSION's ctxloom-controlled
// INSTANCE under paths.HarpSessionEngineHomes — and returns it present or absent, with
// the reason when absent. It creates the instance and prepares it as a side
// effect, so a present result always names a directory that exists and (for
// an engine that authenticates from its env) whose run can authenticate.
//
// THE SCOPING RULE (ruled 2026-09-21):
//
//	Every run gets the controlled per-session home — a bare run, a `run
//	--agent`, a delegated child, a fan-out member, a profile-set launch with
//	no binding at all. Only a binding whose engine_home SELECTS "host" keeps
//	the home its runtime gives it: the REAL host home on the host, a fresh
//	$HOME in a container.
//
// The real home is the UNSAFE selection, never a default: pointing a run at
// the human's ~/.claude hands it the human's credentials, memory, plugins,
// personal MCP registrations, global agents and steering, and lets it write
// session state and settings edits back into them. A binding that wants
// that asks for it by name, and the plan and the launch banner name the
// selection unsafe wherever they show it.
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
// "host" the reason is recorded and nothing else happens — that is the
// binding's selection. When it is "session" and the run still gets no home
// (no session name, an engine with no relocatable home, an instance that
// cannot be created), the reason is said out loud, and an engine nothing in
// the env authenticates (no stored or exported token, no API var) is
// FAIL-LOUD: a ClassIsolation finding, FailAlways, for the
// caller's choke gate, naming the remedies. Handing the engine an empty home
// it cannot authenticate against would trade a working run for a mysterious
// 401, and falling back to the real home would hand it what only the
// binding may select.
func ResolveInTreeAgentHome(reg engine.Registry, in InTreeAgentHome) AgentHomeResolution {
	if in.HomeMode == agents.HomeModeHost {
		return absent("engine_home is %q: the binding selected the home its runtime gives the engine", in.HomeMode)
	}
	if in.Harp == "" {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: this run carries no session name and a config-home instance is per-session; using the runtime's own config home instead", in.Backend)
		return absent("this run carries no session name and a config-home instance is per-session")
	}
	spec, ok := inTreeAgentHomeFor(reg, in.Backend, in.Harp, in.ContainerHome == "")
	if !ok {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: engine_home is %q but %s declares no relocatable config home; using the runtime's own config home instead", in.Backend, agents.HomeModeSession, in.Backend)
		return absent("%s declares no relocatable config home", in.Backend)
	}

	home := spec.Dir
	if spec.Prepare != nil {
		if err := spec.Prepare(in.Cwd); err != nil {
			// NON-DEGRADABLE. The fallback is not "less isolation", it is the
			// SHARED host config home — the agent would read and write the
			// user's real engine credentials and state, which is the precise
			// thing `engine_home: session` was asked for to prevent. Delivery
			// never degrades to a shared home: private is the root, and sharing
			// is only ever something a user selects explicitly.
			strictness.FailAlways(report.KindIsolation, inTreeAgentHomeRemedy,
				"in-tree agent home for %s: %v — refusing to point %s at an unauthenticated %s, and refusing to substitute the SHARED host config home for the per-session one this agent asked for",
				in.Backend, err, spec.EnvVar, home)
			return absent("refusing to point %s at an unauthenticated %s: %v", spec.EnvVar, home, err)
		}
	}
	// Restated after preparation rather than assumed: an engine with no
	// instance-config writer is otherwise handed a variable naming a
	// directory nobody created. 0700 because the tree holds engine config.
	if err := os.MkdirAll(home, 0o700); err != nil {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: cannot create %s (%v); using the runtime's own config home instead", in.Backend, home, err)
		return absent("cannot create %s: %v", home, err)
	}

	var advice present.PathsAdvice = present.Host{}
	if in.ContainerHome != "" {
		// A container path, so joined with forward slashes whatever the
		// host's separator (present.Containerize's own rule for Engine).
		advice = present.Containerize{EngineHome: path.Join(in.ContainerHome, spec.Subdir)}
	}
	paths, mounts := advice.ApplyPaths(present.Paths{EngineHome: present.Root{Host: home}})
	res := AgentHomeResolution{
		Root:  paths.EngineHome,
		Env:   map[string]string{spec.EnvVar: paths.EngineHome.Engine},
		Login: spec.LoginEnv,
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
func BindAgentHome(reg engine.Registry, ws isolation.Workspace, in InTreeAgentHome) AgentHomeResolution {
	in.ContainerHome = isolation.ContainerInstanceHome(ws)
	res := ResolveInTreeAgentHome(reg, in)
	if res.Mount == nil {
		return res
	}
	if err := isolation.MountEngineHome(ws, *res.Mount); err != nil {
		strictness.Fail(report.KindIsolation, inTreeAgentHomeRemedy,
			"in-tree agent home for %s: %v; this run uses the runtime's own config home instead", in.Backend, err)
		return absent("%v", err)
	}
	return res
}

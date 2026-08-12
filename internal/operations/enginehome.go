package operations

import (
	"maps"
	"os"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// InTreeAgentHome is one run's claim on a ctxloom-controlled engine config
// home: everything InTreeAgentHomeEnv needs to decide whether this run gets
// one, and where.
type InTreeAgentHome struct {
	// Backend is the resolved backend name (internal/lm/backends).
	Backend string
	// WorkDir is the run's PROJECT root — the live checkout, not a prepared
	// workspace directory. The controlled home hangs off it.
	WorkDir string
	// ConfigHome is the run's resolved agent binding's EFFECTIVE config-home
	// policy — agents.ConfigHomeProject or agents.ConfigHomeHost when a
	// binding was resolved (operations.ResolvedAgent.ConfigHome, already
	// defaulted), or "" when this run has NO agent binding at all. It is THE
	// scoping condition; see InTreeAgentHomeEnv's doc. Only
	// agents.ConfigHomeProject gets a contribution — "" (no binding) and
	// agents.ConfigHomeHost (an undeclared or explicitly host-declared
	// binding) both mean "keep the real host home".
	ConfigHome string
	// Policy is the resolved isolation policy. Only the in-tree (none) policy
	// gets a contribution; a nil Policy means no policy was resolved at all
	// (an injected-Factory path) and is treated the same as an isolated one.
	Policy isolation.Policy
	// Env is the run env AS ALREADY ASSEMBLED — isolation's own config-home
	// vars plus whatever the session/user set. A var already present here wins
	// outright: this contribution only ever fills gaps.
	Env map[string]string
}

// inTreeAgentHomeFixIt is the fix-it on the one finding this file records. It
// names authentication rather than a runtime, exactly like isolation's
// credentialSeedFixIt, and it names --degraded truthfully: under --degraded
// nothing is contributed, so the run falls back to the engine's real host home.
const inTreeAgentHomeFixIt = "authenticate the engine on this host (e.g. `claude login`) or set its API-key env var, or pass --degraded (env CTXLOOM_DEGRADED=1) to run this agent against the host's own shared config home"

// InTreeAgentHomeEnv returns the config-home env additions an IN-TREE AGENT run
// of in.Backend gets — CLAUDE_CONFIG_DIR / KIRO_HOME pointed at a
// project-scoped, ctxloom-controlled home under paths.EngineStateHome — or nil
// when this run must not get one. It creates the home and seeds it as a side
// effect, so a non-nil result always names a directory that exists and (for an
// engine with seedable credentials) can authenticate.
//
// THE SCOPING RULE, and the reason this is not simply "every in-tree run":
//
//	A run resolved through an agent binding whose EFFECTIVE config_home is
//	"project" gets a controlled home. Every other run — no binding at all, an
//	undeclared binding, or one that declares "host" — keeps the REAL host
//	home.
//
// The controlled-home behaviour is strictly OPT-IN: a binding that never
// mentions config_home resolves to agents.ConfigHomeHost
// (operations.ResolveConfigHome), so declaring the binding at all is not
// enough on its own — an agent that wants its runs kept off the human's real
// ~/.claude or ~/.kiro must say `config_home: project`. A delegated child, a
// fan-out member, a `run --agent` — these ARE ctxloom's processes, and
// pointing an unopted one at the human's ~/.claude or ~/.kiro hands it the
// human's memory, plugins, personal MCP registrations, global agents and
// steering, and lets it write session state and settings edits back into
// them; that is the pollution `config_home: project` exists to let a binding
// opt out of. But nothing takes that on by default — a project that wants it
// asks for it by name, on the binding that wants it.
//
// The human's own interactive session (no agent binding resolved at all —
// no --agent, no default_agent) has no ConfigHome to read in the first
// place and always keeps the real home: relocating a human's own working
// environment out from under their interactive session would be a
// regression dressed as isolation, and there is no binding through which
// they could even opt in.
//
// This DELIBERATELY differs from codex, which relocates CODEX_HOME on every
// in-tree run bound or not, unconditionally. The asymmetry is historical
// fact, not inconsistency: codex never used the real ~/.codex in-tree —
// ctxloom has always pointed it at a project-scoped home — so there was
// nothing of the user's to take away, and no opt-in was ever needed. claude
// and kiro DID use the real host home, so for them the same move is a
// taking, and it happens only for the runs that both are not the human's own
// AND asked for it.
//
// THREE EXCLUSIONS, each independently sufficient to contribute nothing:
//
//   - the effective config_home is not "project" (the rule above — this
//     covers no binding, an undeclared binding, and an explicit "host");
//   - not the in-tree axis — a container run's fresh in-container $HOME already
//     IS the controlled home, and a worktree run's per-agent config home is
//     already provisioned and seeded by internal/lm/isolation, UNCONDITIONALLY
//     (config_home does not reach the worktree axis at all). An in-tree path
//     handed to either would name a directory the boundary cannot see;
//   - the var is already set — isolation's own Env(), or the user's `--env`,
//     wins outright. This fills gaps; it never overrides.
//
// FAIL-LOUD, not silent relocation: when an engine's credentials cannot be
// seeded (no API key and no host credential file), this records a
// ClassIsolation finding for the caller's choke gate and contributes NOTHING.
// Handing the engine an empty home it cannot authenticate against would trade a
// working run for a mysterious 401; falling back to the host home is what
// --degraded then actually does.
func InTreeAgentHomeEnv(in InTreeAgentHome) map[string]string {
	if in.ConfigHome != agents.ConfigHomeProject {
		return nil
	}
	if in.Policy == nil || isolation.Isolated(in.Policy) {
		return nil
	}
	spec, ok := backends.InTreeAgentHomeFor(in.Backend, in.WorkDir)
	if !ok {
		return nil
	}
	if in.Env[spec.EnvVar] != "" {
		return nil
	}

	home := spec.Dir
	if spec.Seed != nil {
		if err := spec.Seed(); err != nil {
			strictness.Fail(strictness.ClassIsolation, inTreeAgentHomeFixIt,
				"in-tree agent home for %s: %v — refusing to point %s at an unauthenticated %s; this run uses the host's own config home instead",
				in.Backend, err, spec.EnvVar, home)
			return nil
		}
	}
	// Restated after seeding rather than assumed: the seed creates the home
	// only on the path where there WAS something to copy, and an engine with no
	// seed at all (kiro) is otherwise handed a variable naming a directory
	// nobody created. 0700 because the tree holds engine config and, for
	// claude, live credential bytes.
	if err := os.MkdirAll(home, 0o700); err != nil {
		clidiag.Warn("ctxloom", "in-tree agent home for %s: cannot create %s (%v); using the host's own config home instead", in.Backend, home, err)
		return nil
	}
	return map[string]string{spec.EnvVar: home}
}

// mergeInTreeAgentHome layers InTreeAgentHomeEnv(in)'s result UNDER workspaceEnv
// and returns the result — the same fill-gaps-never-clobber precedence
// cli/run.go's mergeWorkspaceEnv applies to the isolation env. Belt and braces
// with InTreeAgentHomeEnv's own already-set check: that check is what keeps the
// contribution from being COMPUTED (and its home created and seeded) when
// something else already owns the var; this is what keeps it from winning if it
// somehow was. A nil contribution returns workspaceEnv unchanged, uncopied.
func mergeInTreeAgentHome(workspaceEnv map[string]string, in InTreeAgentHome) map[string]string {
	home := InTreeAgentHomeEnv(in)
	if len(home) == 0 {
		return workspaceEnv
	}
	merged := make(map[string]string, len(home)+len(workspaceEnv))
	maps.Copy(merged, home)
	maps.Copy(merged, workspaceEnv)
	return merged
}

// mergedEnvView is the read-only "what env has this run already been promised"
// map InTreeAgentHomeEnv consults for its already-set check: the isolation
// workspace env plus the caller's own extra env, in the SAME precedence the
// launch tail applies (extra wins). Read-only — never handed on as the run env
// — so the callers' own assembly stays the single writer.
func mergedEnvView(workspaceEnv, extraEnv map[string]string) map[string]string {
	if len(extraEnv) == 0 {
		return workspaceEnv
	}
	if len(workspaceEnv) == 0 {
		return extraEnv
	}
	view := make(map[string]string, len(workspaceEnv)+len(extraEnv))
	maps.Copy(view, workspaceEnv)
	maps.Copy(view, extraEnv)
	return view
}

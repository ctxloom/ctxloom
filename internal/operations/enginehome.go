package operations

import (
	"maps"
	"os"

	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/kiro"
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
	// AgentBound reports whether this run was resolved through an AGENT
	// binding. It is THE scoping condition; see InTreeAgentHomeEnv's doc.
	AgentBound bool
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

// inTreeAgentHome describes one engine's in-tree controlled home: which env var
// relocates it, where that var points for a given project, and how (if at all)
// credentials get into it.
type inTreeAgentHome struct {
	// envVar is the engine's own home-relocation variable.
	envVar string
	// home resolves the value for a project root, through the engine package's
	// OWN StateHome-derived helper — never a join spelled here. That is what
	// keeps the run path and any future home-keyed static writer on one root.
	home func(workDir string) string
	// seed copies host credentials into the controlled home, returning an
	// actionable error when there is nothing to authenticate with. nil for an
	// engine that needs none (kiro: its credentials live in a global sqlite no
	// home var relocates, so a FRESH home stays authenticated).
	seed func(workDir string) error
}

// inTreeAgentHomes is the roster of engines that get a ctxloom-controlled
// config home on the IN-TREE axis. Membership is deliberate, and the absentees
// are the interesting part:
//
//   - codex is ABSENT because it already has one on every axis, resolved by its
//     own resolveCodexProjectDir and seeded by its own Setup. A second
//     contributor here would race the one that already works.
//   - opencode is ABSENT because its only home lever is XDG_CONFIG_HOME /
//     XDG_DATA_HOME — not engine-private. Relocating those moves git's, fish's
//     and every other XDG-aware tool's config for the child too, a blast radius
//     that needs its own decision (home-delivery plan, phase 3).
//   - mock and acp are ABSENT because they have no engine-global home to
//     control.
var inTreeAgentHomes = map[string]inTreeAgentHome{
	config.BackendClaudeCode: {
		envVar: claude.ConfigDirEnv,
		home:   claude.InTreeConfigDir,
		seed: func(workDir string) error {
			// StateHome, not InTreeConfigDir: SeedClaudeHome joins the seed
			// spec's own destSubdir under what it is handed, and lands on
			// InTreeConfigDir exactly. internal/claude's
			// TestInTreeConfigDir_IsTheSeedDestination is what keeps that true.
			_, err := isolation.SeedClaudeHome(claude.StateHome(workDir))
			return err
		},
	},
	kiroBackendName: {
		envVar: kiro.HomeEnv,
		home:   kiro.InTreeHome,
		// No seed, and none possible: kiro's subscription auth lives in a
		// global sqlite under XDG_DATA_HOME that KIRO_HOME does not relocate
		// (internal/kiro.HomeEnv's doc, live-verified against kiro-cli 2.12.1).
		// A fresh KIRO_HOME therefore stays authenticated, and XDG_DATA_HOME is
		// deliberately NOT contributed alongside it: relocating the credential
		// store with nothing to seed into it is precisely the silent
		// logged-out-agent failure the worktree axis' GatedOnCreds machinery
		// refuses to cause.
	},
}

// kiroBackendName is kiro's registered backend name. internal/config declares a
// constant for claude-code only, so this one is spelled here rather than
// silently reaching into internal/kiro for an identifier that package does not
// export.
const kiroBackendName = "kiro"

// InTreeAgentHomeEnv returns the config-home env additions an IN-TREE AGENT run
// of in.Backend gets — CLAUDE_CONFIG_DIR / KIRO_HOME pointed at a
// project-scoped, ctxloom-controlled home under paths.EngineStateHome — or nil
// when this run must not get one. It creates the home and seeds it as a side
// effect, so a non-nil result always names a directory that exists and (for an
// engine with seedable credentials) can authenticate.
//
// THE SCOPING RULE, and the reason this is not simply "every in-tree run":
//
//	An AGENT run gets a controlled home. The human's own session keeps the
//	REAL host home.
//
// A delegated child, a fan-out member, a `run --agent` — these are ctxloom's
// processes, and pointing them at the human's ~/.claude or ~/.kiro hands each
// one the human's memory, plugins, personal MCP registrations, global agents
// and steering, and lets it write session state and settings edits back into
// them. That is the pollution this policy exists to end.
//
// The human's own interactive session is the opposite case. Those files are
// their property and their working environment; relocating them out from under
// an interactive session is a regression dressed as isolation, so an unbound
// run (AgentBound false) gets nothing and behaves exactly as it did before this
// policy existed.
//
// This DELIBERATELY differs from codex, which relocates CODEX_HOME on every
// in-tree run bound or not. The asymmetry is historical fact, not
// inconsistency: codex never used the real ~/.codex in-tree — ctxloom has
// always pointed it at a project-scoped home — so there was nothing of the
// user's to take away. claude and kiro DID use the real host home, so for them
// the same move is a taking, and it is scoped to the runs that are not the
// human's.
//
// THREE EXCLUSIONS, each independently sufficient to contribute nothing:
//
//   - not agent-bound (the rule above);
//   - not the in-tree axis — a container run's fresh in-container $HOME already
//     IS the controlled home, and a worktree run's per-agent config home is
//     already provisioned and seeded by internal/lm/isolation. An in-tree path
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
	if !in.AgentBound {
		return nil
	}
	if in.Policy == nil || isolation.Isolated(in.Policy) {
		return nil
	}
	spec, ok := inTreeAgentHomes[in.Backend]
	if !ok {
		return nil
	}
	if in.Env[spec.envVar] != "" {
		return nil
	}

	home := spec.home(in.WorkDir)
	if spec.seed != nil {
		if err := spec.seed(in.WorkDir); err != nil {
			strictness.Fail(strictness.ClassIsolation, inTreeAgentHomeFixIt,
				"in-tree agent home for %s: %v — refusing to point %s at an unauthenticated %s; this run uses the host's own config home instead",
				in.Backend, err, spec.envVar, home)
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
	return map[string]string{spec.envVar: home}
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

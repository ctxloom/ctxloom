package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/version"
	"github.com/ctxloom/ctxloom/resources"
)

// ctxServer holds shared state used by every SDK-backed tool handler. The
// stdio server builds one per process, its identity read from the ambient env
// (selfIdentityFromEnv). The runner-terminated surface builds one per runner
// inside newRunnerMCPServer, its identity being that runner's own harp and
// cell work dir — so a cell-local tool sees the CELL's identity, never the
// serving process's env.
type ctxServer struct {
	cfg *config.Config
	// dryRun suppresses the startup apply's single write. Starting this
	// server normally REWRITES the project's managed settings — that is what
	// ctxloom does — so this is the way to ask what a start would change
	// without changing it. See operations.ApplyHooksRequest.DryRun.
	dryRun bool
	// self is the caller identity every identity-consuming tool uses: from
	// the credential on the coordinator's HTTP surface, from env on stdio.
	self coord.Identity
	// agents is the coordinator-backed delegation state behind the agent_*
	// tools: pre-bound on identity servers (the runner's relay), and nil on
	// a bare stdio server — which then refuses the tools (delegation())
	// rather than hosting a coordinator of its own.
	agents *agentDelegation
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
}

// mcpServerInstructions tells the client what this reduced MCP surface is for.
// ctxloom keeps only the agent's runtime context tools here; all management is
// CLI-driven (see cmd/hook_inject_context.go's onload preamble for the same
// guidance injected at session start).
var mcpServerInstructions = resources.MustGetPromptText("mcp-server-instructions")

// premiseCatalogInstruction tells an MCP client that conditional guidance exists
// and where to ask for it.
//
// The catalog itself is PULLED, never pushed — docs/architecture/core/premise-selection.md
// holds that ruling. What is pushed here is the POINTER, which is the one part a
// client cannot discover on its own: an agent that does not know the catalog
// exists never asks, and the fragments it would have selected are never learned
// to exist.
//
// The selection wording comes from operations.PremiseSelectionInstruction rather
// than a copy. Its three properties were fixed by measurement, and the apparatus
// that measured them was deliberately removed — so a copy that drifts cannot be
// re-derived back to the original. One source, or the measured one loses.
func premiseCatalogInstruction() string {
	var b strings.Builder
	b.WriteString("\n\n")
	b.WriteString(operations.PremiseSelectionInstruction())
	b.WriteString("\nThe catalog is the `")
	b.WriteString(resourceFragmentsURI)
	b.WriteString("` resource: every conditional fragment,\n")
	b.WriteString("each with its premise and the qualified ref to quote back. Read it when you\n")
	b.WriteString("are ABOUT TO ACT, not once at session start — a premise turns on what you\n")
	b.WriteString("are about to do, so the answer only means something at the moment you have\n")
	b.WriteString("something to match against.\n")
	return b.String()
}

// sessionInstructions renders the server instructions for one caller
// identity (the stdio server's env harp, or a coordinator credential's).
func sessionInstructions(harp string) string {
	instructions := mcpServerInstructions + premiseCatalogInstruction()
	if harp == "" {
		return instructions
	}
	// Tell the LLM its own session name so it can self-reference
	// ("save this as the swift-amber-falcon plan") and so plan-
	// stamping correlates the right harp.
	sessionLine := fmt.Sprintf("\n\nYour session is named `%s`. Refer to it by this name when discussing it with the user.", harp)
	// Resume provenance is a property of THIS serving process's session
	// only, so the env read stays gated on the ambient harp matching.
	if resumed := os.Getenv("CTXLOOM_RESUMED_FROM"); resumed != "" && harp == os.Getenv("CTXLOOM_SESSION_HARP") {
		parts := os.Getenv("CTXLOOM_RESUMED_PARTS")
		if parts == "" {
			parts = "session,tasks"
		}
		sessionLine += fmt.Sprintf(" Resumed from `%s` (restored: %s).", resumed, parts)
	}
	// Point the LLM at this session's plan directory. Implementation and
	// strategy plans belong here (not in an ad-hoc .plan/ dir) so they travel
	// with the session and can be recovered on resume. A session may produce
	// several plans, so each is a separately named file with a .plan.md
	// suffix.
	//
	// The path is paths.HarpPlansDir — the harp's persist/ subdirectory — and
	// NOT the harp top level. Only persist/ is bind-mounted into a
	// containerized run (isolation.Container.sessionStateMounts), so an agent
	// that follows this instruction from inside a container and writes at the
	// top level writes into container-ephemeral overlay space and loses the
	// plan on exit: a successful write, a real file, and zero bytes left
	// behind afterwards. This sentence IS the population source for that
	// failure — every session is told where to put its plans right here — so
	// it is the one place the location has to be right.
	if planDir, perr := paths.HarpPlansDir(harp); perr == nil {
		sessionLine += fmt.Sprintf(" Store implementation/strategy plans as markdown files in this session's plan directory `%s`, each named `<descriptive-name>%s` (e.g. `%s`). That directory is the one that survives a containerized run — plans written elsewhere under the session directory do not. A session may have multiple plans — use distinct names and reference plans by their path.", planDir, paths.PlanFileExt, filepath.Join(planDir, "v1-removal"+paths.PlanFileExt))
	}
	return instructions + sessionLine
}

// ServeStdio is the whole body of `ctxloom mcp serve`: forward-mode
// detection, local startup, and the stdio SDK server. The cobra command in
// internal/cli is wiring onto this and nothing more — it owns only the
// signal-aware context, the cwd, and the fail-loud gate.
//
// gate is the caller's fail-loudly check, run after startup and immediately
// before serving. It exists as a callback because the check constructs
// cli.ExitError — the type cli's Execute() recognises for the exit-3
// fatal-findings contract — which is a cli-layer concern this package must
// not import. A non-nil return aborts before any tool is served; a nil gate
// skips the check. An ACCEPTED forward never reaches it: that process runs
// no startup, so it collects no findings to fail on. A REFUSED forward
// (graceful-egomaniac unit 2: identity/stamp mismatch) is different — it
// falls back to local startup exactly like a session that was never
// forward-triggered at all, so it DOES reach gate.
func ServeStdio(ctx context.Context, cwd string, gate func() error, dryRun bool) error {
	// FORWARD MODE (agentcoord B1.6): when the harness-inherited env names
	// the runner's MCP socket, this whole server is a stdio↔HTTP-over-unix
	// proxy onto it. No local startup (config, sync, hooks) runs — the
	// runner process owns the surface and the coordinator credential; this
	// process holds neither. (The B1 forward-to-coordinator HTTP mode is
	// DELETED: CTXLOOM_COORD_URL/CRED are consumed only by the runner now.)
	if sock := os.Getenv(coord.EnvMCPSocket); sock != "" {
		trigger := forwardTrigger{Kind: triggerEnvVar, Name: coord.EnvMCPSocket}
		if outcome, ferr := runMCPForward(ctx, trigger, sock); outcome == forwardOutcomeServed {
			return ferr
		}
		// forwardOutcomeRefused: identity/stamp verification refused this
		// target (graceful-egomaniac unit 2) and already printed why — fall
		// through to local startup below instead of returning.
	} else if sock, markerPath, derr := probeWellKnownRunner(cwd); derr != nil {
		// HOST-CONTROLLED DISCOVERY (fix/host-controlled-mcp-discovery): the
		// env var above rides a VENDOR-CONTROLLED channel (ACP
		// mcpServers.env) that at least one real adapter drops (codex-acp:
		// honors name/command/args, discards env). Probe the well-known
		// marker a runner publishes UNCONDITIONALLY before ever considering
		// local mode — additive to the env fast path above, never a
		// replacement of it. See mcp_discovery.go for exactly how this
		// tells "should have a runner, fail loud" apart from "legitimately
		// standalone, local is correct" apart from "foreign marker, not
		// mine" (graceful-egomaniac unit 3).
		return derr
	} else if sock != "" {
		trigger := forwardTrigger{Kind: triggerMarker, Name: markerPath}
		if outcome, ferr := runMCPForward(ctx, trigger, sock); outcome == forwardOutcomeServed {
			return ferr
		}
		// forwardOutcomeRefused: same fall-through as the env-var trigger.
	}

	s := &ctxServer{self: selfIdentityFromEnv(cwd), dryRun: dryRun}
	if err := s.startup(ctx); err != nil {
		// startup() only returns context.Canceled — anything else
		// (config load failure, sync errors, hook failures) is
		// handled inline via warnings, per CLAUDE.md fault tolerance.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	// Strict mode: the SDK owns the initialize handshake, so we cannot refuse
	// it per-protocol; instead abort before serving when startup collected a
	// fatal finding. The gate returns the exit-3 ExitError (Execute os.Exit's
	// on it), so the client sees a server that failed to launch rather than
	// one that silently serves empty context. Degraded mode returns nil and
	// serves as before.
	if gate != nil {
		if ferr := gate(); ferr != nil {
			return ferr
		}
	}

	opts := &mcp.ServerOptions{Instructions: sessionInstructions(s.self.Harp)}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: version.Version,
	}, opts)
	s.registerTools(server)
	s.registerResources(server)

	// A signal-driven cancellation is a clean shutdown, not an error.
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// startup runs the same boot sequence the legacy MCP server did, in the
// same order, with the same fault-tolerance semantics:
//
//  1. Load config (warn on failure, use a minimal empty config)
//  2. Auto-sync remote bundles/profiles if enabled
//  3. Apply hooks
//
// Steps 2 and 3 RECORD findings rather than deciding their own fatality; the
// caller's gate aborts the launch when any were fatal, and --degraded lowers
// them to warnings. So "the agent must still come up" is no longer true and is
// not meant to be: a launch that succeeds without doing the thing is worse than
// one that refuses, because nothing downstream can tell the difference.
//
// ORDERING IS LOAD-BEARING. Step 1 and the reporting around it RESOLVE without
// mutating; everything that writes, sweeps or syncs comes after, behind the
// dry-run gate. A write that happened before that gate could not be suppressed
// by it, which is the whole point of the flag.
func (s *ctxServer) startup(ctx context.Context) error {
	cfg := loadStartupConfig()
	s.cfg = cfg

	// Hooks/statusline/MCP entries are written as bare `ctxloom` and
	// resolve via PATH at fire time. Flag the one case that can't catch:
	// a different ctxloom shadowing the running binary on PATH.
	agent.WarnOnCtxloomPathSkew()

	// Log which companion binaries (taskloom, ltk) this session is wired
	// with, version-probed via `<bin> version --format json`. The wiring itself
	// happens in applyStartupHooks below via the built-in bundles.
	operations.ReportCompanions(os.Stderr, cfg.TrustRoot())

	// EVERYTHING BELOW MUTATES, so it is gated as one block. Config
	// resolution and the companion report above do not, which is the
	// ordering this depends on: resolve first, write second, so a dry run
	// still surfaces what a real start would find.
	//
	// The reapers are the reason this gate is not optional. They DELETE
	// worktrees, KILL containers and MOVE authored session files — a
	// "--dry-run" that still reaped would be strictly more destructive than
	// the flag's name admits, and the damage would be to the exact artifacts
	// (an agent's only copy of its work) that are hardest to get back.
	if s.dryRun {
		fmt.Fprintf(os.Stderr,
			"ctxloom: --dry-run: skipping startup reapers, remote sync, and the managed-surface apply; resolving only\n")
		s.applyStartupHooks(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}

	// Startup reaper (bony-carry bug #2): sweep any per-agent worktree
	// checkout left behind by a crashed/killed prior run — see
	// operations.SweepOrphanedWorktrees's doc. Best-effort, silent unless it
	// found something.
	operations.SweepOrphanedWorktrees(ctx, os.Stderr)

	// Startup reaper, container half: sweep any still-RUNNING per-agent
	// runner container left behind by a crashed/killed prior run — teardown
	// is a deferred isolation.RunnerHandle.Kill, which does not survive
	// SIGKILL/OOM/a closed terminal — see operations.SweepOrphanedContainers's
	// doc.
	operations.SweepOrphanedContainers(ctx, os.Stderr)

	// Startup reaper, second half: the per-session engine-home instances a
	// crashed run leaves in THIS project's tree, each holding a copied
	// credential — see operations.SweepOrphanedSessionHomes. Same fault-tolerance, same
	// silence when there is nothing to do.
	operations.SweepOrphanedSessionHomes(os.Stderr)

	// Startup reaper, third half: authored session files (above all the
	// *.plan.md this server's own instructions ask for) left at a harp
	// directory's undurable top level, moved into persist/ where a
	// containerized run's bind mount reaches them — see
	// operations.SweepHarpArtifacts. Live sessions are passed over.
	operations.SweepHarpArtifacts(os.Stderr)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	runStartupSync(ctx, cfg)

	// Apply hooks against the active lockfile. Unreviewed bundle hooks/MCP are
	// withheld per item by the executable trust gate ApplyHooks installs, so
	// changed untrusted content never activates until accepted via `ctxloom
	// review`.
	s.applyStartupHooks(ctx)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// loadStartupConfig loads config, falling back to a minimal empty config on
// failure (startup must never abort on config errors — CLAUDE.md), and echoes
// any accumulated config warnings to stderr.
func loadStartupConfig() *config.Config {
	return loadStartupConfigWith(config.Load)
}

// loadStartupConfigWith is loadStartupConfig over an injected loader. config.Load
// degrades essentially every user-facing failure to a config.Warning rather than
// an error, so the error branch below is otherwise unreachable from a fixture —
// and unreachable code is exactly where a reporting defect survives.
func loadStartupConfigWith(load func(...config.LoadOption) (*config.Config, error)) *config.Config {
	cfg, err := load()
	if err != nil {
		cfg = fallbackConfigForLoadFailure(err)
	}
	config.RecordWarningsTo(os.Stderr, cfg.GetWarnings())
	return cfg
}

// fallbackConfigForLoadFailure builds the minimal config a failed load degrades
// to. The failure rides as the config's single Warning, which is the ONE report
// of it: config.RecordWarningsTo both prints that warning and records it as a
// strictness finding, so warning about it here too would put the identical
// sentence on stderr twice.
func fallbackConfigForLoadFailure(err error) *config.Config {
	return config.NewFixture(config.Fixture{
		LM:       config.LMConfig{Configs: make(map[string]config.LLMConfig)},
		Warnings: []config.Warning{{Kind: config.WarnKindRead, Text: fmt.Sprintf("failed to load config: %v", err)}},
	})
}

// runStartupSync auto-syncs remote bundles/profiles when enabled, bounded to
// 60s.
//
// A failure is REPORTED as a ClassSync finding, not swallowed: the startup gate
// decides its fatality, so by default the launch aborts and --degraded warns and
// continues. Cancellation alone returns quietly. This is the refuse-by-default
// posture, not the older always-launch one — a sync that silently continued was
// how a session came up against content nobody could see had failed to load.
func runStartupSync(ctx context.Context, cfg *config.Config) {
	syncCfg := cfg.GetSyncConfig()
	if !syncCfg.ShouldAutoSync() {
		return
	}
	fmt.Fprintf(os.Stderr, "ctxloom: syncing remote bundles and profiles from config...\n")
	syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
	result, syncErr := operations.SyncOnStartup(syncCtx, cfg)
	syncCancel()
	if syncErr != nil {
		if !errors.Is(syncErr, context.Canceled) {
			strictness.Fail(strictness.ClassSync, "check the remote/network, or pass --degraded to launch anyway", "sync failed: %v", syncErr)
		}
		return
	}
	operations.WriteAndRecordSyncSummary(os.Stderr, result)
}

// applyStartupHooks runs the ApplyHooks startup phase against the active
// lockfile. ApplyHooks installs the executable trust gate, so unreviewed bundle
// hooks/MCP are withheld per item until accepted via `ctxloom review`.
func (s *ctxServer) applyStartupHooks(ctx context.Context) {
	// "all": the server doesn't know which agent hosts it, and every backend
	// with settings must see the same regenerated context/hooks/commands —
	// refreshing only one leaves the others serving stale managed sets.
	if _, err := operations.ApplyHooks(ctx, operations.ApplyHooksRequest{
		RegenerateContext: true,
		DryRun:            s.dryRun,
	}); err != nil && !errors.Is(err, context.Canceled) {
		clidiag.Warn("ctxloom", "failed to apply hooks: %v", err)
	}
}

// registerTools wires every ctxloom tool into the SDK server.
//
// Phase 4 removed five entire register*Tools functions (fragments,
// profiles, prompts, mcp servers, remotes). Listings from those domains
// now ride on MCP resources (ctxloom://fragments, profiles, prompts,
// remotes, mcp-servers — see registerResources); writes are CLI-only
// via the existing cobra surface. Their tool files were deleted.
//
// Each per-category register call lives in its own file. registerTools
// is the only thing the SDK server needs at construction.
func (s *ctxServer) registerTools(server *mcp.Server) {
	s.registerContextTools(server)
	s.registerContextStatusTool(server)
	s.registerMemoryTools(server)
	s.registerAgentTools(server)
	s.registerTriggerTools(server)
}

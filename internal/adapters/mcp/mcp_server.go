package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/shared/version"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// ctxServer holds shared state used by every SDK-backed tool handler. The
// stdio server builds one per process, its identity read from the ambient env
// (selfIdentityFromEnv). The plugin-hosted owner arm's socket endpoint
// (ServeRunnerMCP) builds one per runner as the config-backed local half of
// runnermcp.NewServer, its identity being that runner's own harp and cwd — so
// a cell-local tool sees the CELL's identity, never the serving process's env.
type ctxServer struct {
	// app is the process's composition; cfg is the generation this server
	// serves — the one published after startup's sync, held for the
	// server's life. The owner arm's server (ServeRunnerMCP) is handed its
	// generation and holds no app.
	app *operations.App
	// build constructs the coordinator a bare `ctxloom mcp` stands up on its
	// first agent_run — the composition root's constructor, handed in.
	build CoordinatorConstructor
	cfg   *config.Config
	// dryRun suppresses the startup apply's single write. Starting this
	// server normally REWRITES the project's managed settings — that is what
	// ctxloom does — so this is the way to ask what a start would change
	// without changing it. See operations.ApplyHooksRequest.DryRun.
	dryRun bool
	// self is the caller identity every identity-consuming tool uses: from
	// the credential on the coordinator's HTTP surface, from env on stdio.
	self coord.Identity
	// agents is the coordinator-backed delegation state behind the agent_*
	// tools; nil until first use on a bare stdio server (lazy standup in
	// delegation()), pre-bound on identity servers.
	agents   *agentDelegation
	agentsMu sync.Mutex
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

// ServeStdio is the whole body of `ctxloom mcp serve`: forward-mode
// detection, local startup, and the stdio SDK server. The cobra command in
// internal/adapters/cli is wiring onto this and nothing more — it owns only the
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
// strictness is the posture this server's composition runs under; a server
// built without an App (a test double) runs strict.
func (s *ctxServer) strictness() strictness.Mode {
	if s.app == nil {
		return strictness.Mode{Prog: "ctxloom"}
	}
	return s.app.Strictness
}

func ServeStdio(ctx context.Context, app *operations.App, build CoordinatorConstructor, cwd string, gate func() error, dryRun bool) error {
	// FORWARD MODE: when the engine-inherited env names the plugin-hosted
	// owner arm's runner socket, this whole server is a stdio↔HTTP-over-unix
	// proxy onto it. No local startup (config, sync, hooks) runs — the runner
	// process owns the surface and the coordinator credential; this process
	// holds neither. A refusal (identity/stamp mismatch, already printed)
	// falls through to local startup.
	if sock := os.Getenv(coord.EnvMCPSocket); sock != "" {
		if outcome, ferr := runMCPForward(ctx, forwardTrigger{Name: coord.EnvMCPSocket}, sock); outcome == forwardOutcomeServed {
			return ferr
		}
	}

	s := &ctxServer{app: app, build: build, self: selfIdentityFromEnv(cwd), dryRun: dryRun}
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

	opts := &mcp.ServerOptions{Instructions: operations.SessionInstructions(s.self.Harp)}
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
	cfg, err := s.app.Config(ctx)
	if err != nil {
		return err
	}
	config.ReportWarnings(strictness.Sink("ctxloom"), cfg.GetWarnings())
	s.cfg = cfg

	// Hooks/statusline/MCP entries are written as bare `ctxloom` and
	// resolve via PATH at fire time. Flag the one case that can't catch:
	// a different ctxloom shadowing the running binary on PATH.
	agent.WarnOnCtxloomPathSkew(report.To(strictness.Sink("ctxloom")))

	// Log which companion binaries (taskloom, ltk) this session is wired
	// with, version-probed via `<bin> version --format json`. The wiring itself
	// happens in applyStartupHooks below via the built-in bundles.
	operations.ReportCompanions(os.Stderr, s.app.Prober(), cfg.TrustRoot())

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

	if ctx.Err() != nil {
		return ctx.Err()
	}

	runStartupSync(ctx, s.app)
	// A startup sync that pulled published a new generation; the server
	// serves that one.
	if cfg, err = s.app.Config(ctx); err != nil {
		return err
	}
	s.cfg = cfg

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

func runStartupSync(ctx context.Context, app *operations.App) {
	cfg, err := app.Config(ctx)
	if err != nil {
		return
	}
	syncCfg := cfg.GetSyncConfig()
	if !syncCfg.ShouldAutoSync() {
		return
	}
	fmt.Fprintf(os.Stderr, "ctxloom: syncing remote bundles and profiles from config...\n")
	syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
	result, syncErr := operations.SyncOnStartup(syncCtx, app)
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
		Cfg:               s.cfg,
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

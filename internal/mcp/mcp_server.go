package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
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

// mcpServerInstructions tells the client what this reduced MCP surface is for.
// ctxloom keeps only the agent's runtime context tools here; all management is
// CLI-driven (see cmd/hook_inject_context.go's onload preamble for the same
// guidance injected at session start).
var mcpServerInstructions = resources.MustGetPromptText("mcp-server-instructions")

// sessionInstructions renders the server instructions for one caller
// identity (the stdio server's env harp, or a coordinator credential's).
func sessionInstructions(harp string) string {
	instructions := mcpServerInstructions
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
	// Point the LLM at this session's directory for plans. Implementation
	// and strategy plans belong here (not in an ad-hoc .plan/ dir) so they
	// travel with the session and can be recovered on resume. A session
	// may produce several plans, so each is a separately named file with a
	// .plan.md suffix sitting directly in the session directory.
	if sessDir, perr := paths.HarpDir(harp); perr == nil {
		sessionLine += fmt.Sprintf(" Store implementation/strategy plans as markdown files in this session's directory `%s`, each named `<descriptive-name>%s` (e.g. `%s`). A session may have multiple plans — use distinct names and reference plans by their path.", sessDir, paths.PlanFileExt, filepath.Join(sessDir, "v1-removal"+paths.PlanFileExt))
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
// skips the check. Neither forward path reaches it: those processes run no
// startup, so they collect no findings to fail on.
func ServeStdio(ctx context.Context, cwd string, gate func() error) error {
	// FORWARD MODE (agentcoord B1.6): when the harness-inherited env names
	// the runner's MCP socket, this whole server is a stdio↔HTTP-over-unix
	// proxy onto it. No local startup (config, sync, hooks) runs — the
	// runner process owns the surface and the coordinator credential; this
	// process holds neither. (The B1 forward-to-coordinator HTTP mode is
	// DELETED: CTXLOOM_COORD_URL/CRED are consumed only by the runner now.)
	if sock := os.Getenv(coord.EnvMCPSocket); sock != "" {
		return runMCPForward(ctx, sock)
	}

	// HOST-CONTROLLED DISCOVERY (fix/host-controlled-mcp-discovery): the env
	// var above rides a VENDOR-CONTROLLED channel (ACP mcpServers.env) that
	// at least one real adapter drops (codex-acp: honors name/command/args,
	// discards env). Probe the well-known marker a runner publishes
	// UNCONDITIONALLY before ever considering local mode — additive to the
	// env fast path above, never a replacement of it. See mcp_discovery.go
	// for exactly how this tells "should have a runner, fail loud" apart
	// from "legitimately standalone, local is correct".
	if sock, derr := probeWellKnownRunner(cwd); derr != nil {
		return derr
	} else if sock != "" {
		return runMCPForward(ctx, sock)
	}

	s := &ctxServer{self: selfIdentityFromEnv(cwd)}
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
//  2. Auto-sync remote bundles/profiles if enabled (warn on failure)
//  3. Apply hooks (warn on failure)
//
// Any step failing produces a stderr warning but doesn't abort startup —
// the agent must still come up. The only abort path is ctx.Done().
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
	operations.ReportCompanions(os.Stderr)

	// Startup reaper (bony-carry bug #2): sweep any per-agent worktree
	// checkout left behind by a crashed/killed prior run — see
	// operations.SweepOrphanedWorktrees's doc. Best-effort, silent unless it
	// found something.
	operations.SweepOrphanedWorktrees(ctx, os.Stderr)

	// Startup reaper, second half: the per-session engine-home instances a
	// crashed run leaves in THIS project's tree, each holding a copied
	// credential — see operations.SweepOrphanedSessionHomes. Same fault-tolerance, same
	// silence when there is nothing to do.
	operations.SweepOrphanedSessionHomes(os.Stderr)

	purgeLegacyBundles(cfg)

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
		Profiles: config.ProfilesConfig{Definitions: make(map[string]config.Profile)},
		Warnings: []config.Warning{{Kind: config.WarnKindRead, Text: fmt.Sprintf("failed to load config: %v", err)}},
	})
}

// purgeLegacyBundles removes leftover extracted bundle YAML copies from the
// pre-PR-1 era (docs/bundle-review-plan.md Phase 1.4). With the read path now
// served from the git clone cache via remote.BundleReader, those files would
// otherwise shadow the SHA-pinned content. Local bundles (no `_source`
// metadata) are preserved. Warn-and-continue: cleanup failure must not abort.
func purgeLegacyBundles(cfg *config.Config) {
	if removed, err := operations.PurgeExtractedBundles(cfg); err != nil {
		clidiag.Warn("ctxloom", "legacy bundle cleanup failed: %v", err)
	} else if removed > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: removed %d legacy extracted bundle YAML(s)\n", removed)
	}
}

// runStartupSync auto-syncs remote bundles/profiles when enabled. The sync is
// bounded to 60s and fully fault-tolerant: a failure (other than cancellation)
// warns and continues so the agent still comes up (CLAUDE.md).
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
		Backend:           "all",
		RegenerateContext: true,
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

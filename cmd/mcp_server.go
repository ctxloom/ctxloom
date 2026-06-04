package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/resources"
)

// ctxServer holds shared state used by every SDK-backed tool handler.
// One instance per process — the cfg is loaded during startup and stays
// for the lifetime of the MCP session.
type ctxServer struct {
	cfg *config.Config
}

// mcpServerInstructions tells the client what this reduced MCP surface is for.
// ctxloom keeps only the agent's runtime context tools here; all management is
// CLI-driven (see cmd/hook_inject_context.go's onload preamble for the same
// guidance injected at session start).
var mcpServerInstructions = resources.MustGetPromptText("mcp-server-instructions")

// runMCPServerSDK is the cobra RunE for `ctxloom mcp serve`. The SDK's
// Server.Run handles its own stdin EOF and ctx-cancellation cleanup — we
// just need to give it a signal-aware context. signal.NotifyContext is
// the idiomatic Go-1.16+ replacement for manual sigCh + cancel goroutines.
func runMCPServerSDK(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	s := &ctxServer{}
	if err := s.startup(ctx); err != nil {
		// startup() only returns context.Canceled — anything else
		// (config load failure, sync errors, hook failures) is
		// handled inline via warnings, per CLAUDE.md fault tolerance.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	instructions := mcpServerInstructions
	if harp := os.Getenv("CTXLOOM_SESSION_HARP"); harp != "" {
		// Tell the LLM its own session name so it can self-reference
		// ("save this as the swift-amber-falcon plan") and so plan-
		// stamping correlates the right harp.
		sessionLine := fmt.Sprintf("\n\nYour session is named `%s`. Refer to it by this name when discussing it with the user.", harp)
		if resumed := os.Getenv("CTXLOOM_RESUMED_FROM"); resumed != "" {
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
		instructions += sessionLine
	}
	opts := &mcp.ServerOptions{Instructions: instructions}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: Version,
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
	backends.WarnOnCtxloomPathSkew()

	purgeLegacyBundles(cfg)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	runStartupSync(ctx, cfg)

	// Always check for leftover pending state from a previous session,
	// regardless of whether we just synced. Otherwise a user who closes
	// ctxloom without acknowledging review would silently bypass the
	// gate on restart. This call also covers the post-sync path: if the
	// sync just wrote to pending, the diff picks it up here.
	s.handleSyncChanges(ctx, operations.PendingBundleChanges(cfg))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Apply hooks against the active (approved) lockfile. ApplyHooks never
	// reads pending content, so unreviewed bundle hooks never run here.
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config: %v\n", err)
		cfg = &config.Config{
			LM:       config.LMConfig{Configs: make(map[string]config.LLMConfig)},
			Profiles: config.ProfilesConfig{Definitions: make(map[string]config.Profile)},
			Warnings: []string{fmt.Sprintf("failed to load config: %v", err)},
		}
	}
	for _, warning := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", warning)
	}
	return cfg
}

// purgeLegacyBundles removes leftover extracted bundle YAML copies from the
// pre-PR-1 era (docs/bundle-review-plan.md Phase 1.4). With the read path now
// served from the git clone cache via remote.BundleReader, those files would
// otherwise shadow the SHA-pinned content. Local bundles (no `_source`
// metadata) are preserved. Warn-and-continue: cleanup failure must not abort.
func purgeLegacyBundles(cfg *config.Config) {
	if removed, err := operations.PurgeExtractedBundles(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: legacy bundle cleanup failed: %v\n", err)
	} else if removed > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: removed %d legacy extracted bundle YAML(s)\n", removed)
	}
}

// runStartupSync auto-syncs remote bundles/profiles when enabled. The sync is
// bounded to 60s and fully fault-tolerant: a failure (other than cancellation)
// warns and continues so the agent still comes up (CLAUDE.md).
func runStartupSync(ctx context.Context, cfg *config.Config) {
	if !cfg.Sync.ShouldAutoSync() {
		return
	}
	fmt.Fprintf(os.Stderr, "ctxloom: syncing remote bundles and profiles from config...\n")
	syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
	result, syncErr := operations.SyncOnStartup(syncCtx, cfg)
	syncCancel()
	if syncErr != nil {
		if !errors.Is(syncErr, context.Canceled) {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", syncErr)
		}
		return
	}
	writeSyncSummary(os.Stderr, result)
}

// handleSyncChanges reports bundle changes that sync left pending. Bundle review
// is no longer an in-chat gate: untrusted changes wait for an explicit CLI review
// (`ctxloom bundle review`) and the server never blocks on them. The only action
// taken here is the non-interactive bypass: with CTXLOOM_AUTO_APPROVE_BUNDLES=1
// (CI/cron), pending changes are merged into active immediately.
func (s *ctxServer) handleSyncChanges(_ context.Context, changes *operations.BundleChangeSet) {
	if changes.IsEmpty() {
		return
	}
	if reviewBypassed() {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s=1, auto-approving %d bundle change(s):\n", reviewBypassEnvVar, len(changes.All()))
		for _, c := range changes.All() {
			if c.OldSHA == "" {
				fmt.Fprintf(os.Stderr, "  + %s @ %s (from %s)\n", c.Name, shortSHA(c.NewSHA), c.Remote)
			} else {
				fmt.Fprintf(os.Stderr, "  ~ %s %s → %s (from %s)\n", c.Name, shortSHA(c.OldSHA), shortSHA(c.NewSHA), c.Remote)
			}
		}
		if err := operations.MergePendingLockfile(s.cfg); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: merge pending lockfile: %v\n", err)
		}
		return
	}
	fmt.Fprintf(os.Stderr, "ctxloom: %d bundle change(s) pending review — run `ctxloom bundle review`\n", len(changes.All()))
}

// applyStartupHooks runs the ApplyHooks startup phase against the active
// (approved) lockfile. ApplyHooks never reads pending content, so unreviewed
// bundle hooks are not executed — they only run after a CLI approval merges
// them into active.
func (s *ctxServer) applyStartupHooks(ctx context.Context) {
	if _, err := operations.ApplyHooks(ctx, s.cfg, operations.ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
	}); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to apply hooks: %v\n", err)
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
	s.registerMemoryTools(server)
	s.registerTaskTools(server)
}

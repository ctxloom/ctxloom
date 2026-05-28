package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// ctxServer holds shared state used by every SDK-backed tool handler.
// One instance per process — the cfg is loaded during startup and stays
// for the lifetime of the MCP session.
type ctxServer struct {
	cfg    *config.Config
	review *bundleReviewState
}

// runMCPServerSDK is the cobra RunE for `ctxloom mcp serve`. The SDK's
// Server.Run handles its own stdin EOF and ctx-cancellation cleanup — we
// just need to give it a signal-aware context. signal.NotifyContext is
// the idiomatic Go-1.16+ replacement for manual sigCh + cancel goroutines.
func runMCPServerSDK(_ *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	s := &ctxServer{review: &bundleReviewState{}}
	if err := s.startup(ctx); err != nil {
		// startup() only returns context.Canceled — anything else
		// (config load failure, sync errors, hook failures) is
		// handled inline via warnings, per CLAUDE.md fault tolerance.
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	// Append the review-protocol instructions to ServerOptions.Instructions
	// regardless of current pending state, so clients reading initialize
	// see the rules and can react when pending becomes non-empty later in
	// the session. The middleware below is what actually enforces them.
	instructions := reviewInstructionsBlock
	if harp := os.Getenv("CTXLOOM_SESSION_HARP"); harp != "" {
		// Tell the LLM its own session name so it can self-reference
		// ("save this as the swift-amber-falcon plan") and so plan-
		// stamping / TodoWrite captures correlate the right harp.
		sessionLine := fmt.Sprintf("\n\nYour session is named `%s`. Refer to it by this name when discussing it with the user.", harp)
		if resumed := os.Getenv("CTXLOOM_RESUMED_FROM"); resumed != "" {
			parts := os.Getenv("CTXLOOM_RESUMED_PARTS")
			if parts == "" {
				parts = "session,tasks"
			}
			sessionLine += fmt.Sprintf(" Resumed from `%s` (restored: %s).", resumed, parts)
		}
		instructions += sessionLine
		// Canonical, greppable marker for `session distill`'s raw-transcript
		// scan (see harpSessionMarker / discoverSessionByHarpName). On its own
		// line and distinct from the prose above so discovery never depends on
		// wording. Lands in the transcript as part of this instructions block.
		instructions += "\n\n" + harpSessionMarker(harp)
	}
	opts := &mcp.ServerOptions{Instructions: instructions}
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "ctxloom",
		Version: Version,
	}, opts)
	s.installReviewMiddleware(server)
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: failed to load config: %v\n", err)
		cfg = &config.Config{
			LM:       config.LMConfig{Plugins: make(map[string]config.PluginConfig)},
			Profiles: make(map[string]config.Profile),
			Warnings: []string{fmt.Sprintf("failed to load config: %v", err)},
		}
	}
	s.cfg = cfg

	for _, warning := range cfg.Warnings {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: %s\n", warning)
	}

	// Hooks/statusline/MCP entries are written as bare `ctxloom` and
	// resolve via PATH at fire time. Flag the one case that can't catch:
	// a different ctxloom shadowing the running binary on PATH.
	backends.WarnOnCtxloomPathSkew()

	// Purge any leftover extracted bundle YAML copies from the pre-PR-1
	// era (docs/bundle-review-plan.md Phase 1.4). With the read path now
	// served from the git clone cache via remote.BundleReader, those files
	// would otherwise shadow the SHA-pinned content. Local bundles (no
	// `_source` metadata) are preserved.
	if removed, err := operations.PurgeExtractedBundles(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: legacy bundle cleanup failed: %v\n", err)
	} else if removed > 0 {
		fmt.Fprintf(os.Stderr, "ctxloom: removed %d legacy extracted bundle YAML(s)\n", removed)
	}

	if ctx.Err() != nil {
		return ctx.Err()
	}

	if cfg.Sync.ShouldAutoSync() {
		fmt.Fprintf(os.Stderr, "ctxloom: syncing remote bundles and profiles from config...\n")
		syncCtx, syncCancel := context.WithTimeout(ctx, 60*time.Second)
		result, syncErr := operations.SyncOnStartup(syncCtx, cfg)
		syncCancel()
		if syncErr != nil {
			if !errors.Is(syncErr, context.Canceled) {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: sync failed: %v\n", syncErr)
			}
		} else {
			writeSyncSummary(os.Stderr, result)
		}
	}

	// Always check for leftover pending state from a previous session,
	// regardless of whether we just synced. Otherwise a user who closes
	// ctxloom without acknowledging review would silently bypass the
	// gate on restart. This call also covers the post-sync path: if the
	// sync just wrote to pending, the diff picks it up here.
	s.handleSyncChanges(ctx, operations.PendingBundleChanges(cfg))

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Apply hooks only when no review is pending — bundle-shipped hook
	// scripts can execute arbitrary code and MUST NOT run against
	// unreviewed bundle content (docs/bundle-review-plan.md Phase 3.3).
	s.applyHooksIfNotPending(ctx)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// handleSyncChanges decides what to do with the BundleChangeSet produced
// by SyncOnStartup. Three branches:
//
//   - Empty: nothing to do, hooks run normally.
//   - Bypass env var set: stderr warning listing changes, merge pending
//     into active immediately, hooks run normally.
//   - Otherwise: populate review state, defer hooks until acknowledge/
//     decline/trust clears pending.
func (s *ctxServer) handleSyncChanges(ctx context.Context, changes *operations.BundleChangeSet) {
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
		_ = ctx
		return
	}
	s.review.set(changes)
	fmt.Fprintf(os.Stderr, "ctxloom: %d bundle change(s) awaiting review (hooks deferred)\n", len(changes.All()))
}

// applyHooksIfNotPending runs the ApplyHooks startup phase, but only when
// no bundle review is outstanding. Hook scripts can execute arbitrary code
// shipped from bundles; running them before the user approves the new
// content would defeat the review purpose. Called from startup() (initial
// pass) and from the review-clearing tool handlers (acknowledge / decline /
// trust) whenever pending transitions to empty.
func (s *ctxServer) applyHooksIfNotPending(ctx context.Context) {
	if s.review.hasPending() {
		return
	}
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
	s.registerBundleTools(server)
	s.registerHooksTools(server)
	s.registerSyncTools(server)
	s.registerMemoryTools(server)
	s.registerReviewTools(server)
	s.registerTaskTools(server)
}

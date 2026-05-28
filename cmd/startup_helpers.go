package cmd

import (
	"io"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/iox"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// loadConfigOrFallback loads ctxloom config via loader, falling back to a
// minimal config rooted at .ctxloom when loading fails. The fallback path
// emits a stderr-style warning to w so users in unconfigured directories
// can still diagnose why their effective config is incomplete.
//
// Used by tools that must keep working without a fully-loaded config
// (`ctxloom remote update`, `ctxloom search`) per CLAUDE.md fault tolerance.
func loadConfigOrFallback(loader func() (*config.Config, error), w io.Writer) *config.Config {
	cfg, err := loader()
	if err != nil {
		// Best-effort warning. This runs in fault-tolerant startup paths
		// (`ctxloom remote update`, `ctxloom search`) that must proceed
		// regardless, so a failed warning write has nowhere to go and is
		// intentionally dropped (captured-but-unchecked via iox.ErrWriter).
		ew := iox.NewErrWriter(w)
		ew.Printf("ctxloom: warning: failed to load config (%v); using minimal default rooted at .ctxloom\n", err)
		return &config.Config{AppPaths: []string{".ctxloom"}}
	}
	return cfg
}

// writeSyncSummary prints a consistent summary of a SyncDependenciesResult
// to w. Both `ctxloom mcp` startup and `ctxloom run` use this so users see
// the same surface regardless of how sync was triggered.
//
//   - Successful syncs that installed or updated something get a one-line
//     status message.
//   - Failures get a "completed with N errors" warning plus a per-item
//     breakdown so the user can diagnose which bundle/profile failed and
//     why (important for hard-fail clone errors that used to silently
//     degrade to API).
//
// A nil result is a no-op so callers don't have to nil-check.
func writeSyncSummary(w io.Writer, result *operations.SyncDependenciesResult) {
	if result == nil {
		return
	}
	// Best-effort summary. Both `ctxloom mcp` startup and `ctxloom run`
	// call this on a fault-tolerant path that must never block, so a
	// failed write to the summary target is intentionally dropped
	// (captured-but-unchecked via iox.ErrWriter).
	ew := iox.NewErrWriter(w)
	if result.Status != "up_to_date" && result.Installed+result.Updated > 0 {
		ew.Printf("ctxloom: %s\n", result.Message)
	}
	if result.Errors > 0 {
		ew.Printf("ctxloom: warning: sync completed with %d errors\n", result.Errors)
		for _, item := range result.Failed {
			ew.Printf("ctxloom:   - %s (%s): %s\n", item.Reference, item.Type, item.Error)
		}
	}
}

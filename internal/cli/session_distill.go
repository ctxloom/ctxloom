package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// The distillation/compaction cluster the session commands share with the MCP
// memory tools: situating a one-shot process in a session's own project dir,
// then running the compactor for one entry. operations.CompactEntry is the
// single funnel every distill path goes through.

// distillMissingOrStale distills every entry whose essence is missing or stale
// (SourceStale), so `session list --distill` shows a title on every row. Each
// session is compacted in its own project dir — the legacy transcript reader is
// cwd-bound — so the loop chdir's per entry and restores the original cwd on
// return. Per-entry failures are warned and skipped: a session that can't be
// distilled (e.g. a legacy-only session with no reachable transcript) must not
// block the listing (CLAUDE.md — a usable partial listing beats a hard fail).
func distillMissingOrStale(cmd *cobra.Command, entries []sessions.Entry, appDir string) {
	origWd, _ := os.Getwd()
	defer func() {
		if origWd != "" {
			_ = os.Chdir(origWd)
		}
	}()
	progress := iox.NewErrWriter(cmd.ErrOrStderr())
	for i := range entries {
		e := &entries[i]
		_, distilled := operations.SessionEssenceInfo(e.HarpName, e, appDir)
		stale, known := e.SourceStale()
		knownStale := known && stale
		if distilled && !knownStale {
			continue // fresh essence already present
		}
		// Situate in the entry's own project dir before loading config /
		// reading the transcript (see runSessionDistill for why chdir is
		// required and safe for a one-shot CLI) — or back in origWd when
		// this entry has none of its own (leaving the PREVIOUS
		// entry's chdir in place here meant config.Load() silently read the
		// wrong project's config for THIS entry, using another project's
		// cwd-bound legacy LLM/backend settings for a distillation that
		// never intended to touch it at all).
		if cerr := situateForEntry(e, origWd); cerr != nil {
			clidiag.Warn("ctxloom", "could not enter project dir %q for %s: %v", e.ProjectDir, e.HarpName, cerr)
			continue
		}
		cfg, cErr := config.Load()
		if cErr != nil {
			clidiag.Warn("ctxloom", "could not load config to distill %s: %v", e.HarpName, cErr)
			continue
		}
		if _, dErr := operations.CompactEntry(cmd.Context(), e, cfg, "", progress); dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s: %v", e.HarpName, dErr)
		}
	}
}

// situateForEntry chdirs the process to e's own ProjectDir, or back to
// origWd when e has none — the shared cwd-management step distillMissingOrStale
// needs before every config.Load()/operations.CompactEntry call, extracted so it is
// independently testable rather than living as an inline branch
// that only ever changed directory FORWARD and never restored it for an
// entry with no ProjectDir of its own. A no-op when the process is already
// in the wanted directory.
func situateForEntry(e *sessions.Entry, origWd string) error {
	want := e.ProjectDir
	if want == "" {
		want = origWd
	}
	if want == "" {
		return nil // no ProjectDir and no resolvable origWd — nothing to situate
	}
	if cwd, err := os.Getwd(); err == nil && cwd == want {
		return nil
	}
	return os.Chdir(want)
}

// compactEntryFn is operations.CompactEntry behind a package var so a
// caller's wiring — notably the context bound a long-running distillation is
// handed — can be observed in a test (mcp_tools_memory_budget_test.go).
// Production never reassigns it; operations must not inherit this cli-only
// test seam, so it stays here rather than moving with CompactEntry itself.
var compactEntryFn = operations.CompactEntry

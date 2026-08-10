package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/transcript/policy"
)

// The distillation/compaction cluster the session commands share with the MCP
// memory tools: situating a one-shot process in a session's own project dir,
// resolving the compaction model, running the compactor for one entry, and
// resolving a transcript source for a backend. compactEntry is the single
// funnel every distill path goes through.

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
		_, distilled := sessionEssenceInfo(e.HarpName, e, appDir)
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
		if _, dErr := compactEntry(cmd.Context(), e, cfg, "", progress); dErr != nil {
			clidiag.Warn("ctxloom", "could not distill %s: %v", e.HarpName, dErr)
		}
	}
}

// situateForEntry chdirs the process to e's own ProjectDir, or back to
// origWd when e has none — the shared cwd-management step distillMissingOrStale
// needs before every config.Load()/compactEntry call, extracted so it is
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

// compactionModelFor resolves the model one distill runs with: an explicit
// caller override wins, and "" falls back to the configured compaction model.
// Kept as a named function despite the single call site (compactEntry, which
// is itself the single funnel) because it is the one unit-testable statement
// of the override rule — honoring the override on only some distill paths
// is exactly the bug this exists to prevent — and compactEntry needs a live
// session entry and compactor to exercise.
func compactionModelFor(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.GetCompactionModel()
}

// compactEntry runs the compactor for a single session entry and returns the
// result. It does NOT change the working directory and does NOT load config —
// the caller supplies cfg and situates the process. This split lets the
// one-shot CLI (`session distill`, `session list --distill`) chdir into the
// entry's project dir first, so the cwd-bound legacy transcript reader
// resolves, while the long-lived MCP server — which must never chdir — calls
// it as-is: canonical-transcript sessions resolve cwd-independently through
// WorkDir, and legacy-only sessions there degrade to the clear "nothing to
// distill" error below rather than corrupting the server's cwd.
//
// session_id is recorded forward by the `ctxloom hook session-bind`
// SessionStart hook (see sessionBindCmd). A container-runtime harp's bind hook
// runs INSIDE the container, though, and the host session index is not mounted
// in — so its session_id never gets bound host-side even though its transcript
// IS reachable (operations.GetSession resolved entry.TranscriptPath via
// fillTranscriptByLocation). Load by that path instead of failing; a canonical
// transcript (a oneshot Execute run's own transcript.jsonl, resolved by
// HarpName inside pb.NewCanonicalFallbackSource) needs no preload. Only
// hard-error when there is neither a bound id, a transcript path, nor a
// captured transcript — genuinely nothing to distill.
// model overrides the compaction model for THIS call; "" uses
// cfg.GetCompactionModel(). It exists so a caller-supplied model override
// reaches the canonical/harp distill path too, not just the backend one —
// the same "empty means config default" shape distillSession already uses.
// compactEntryFn is compactEntry behind a package var so a caller's wiring —
// notably the context bound a long-running distillation is handed — can be
// observed in a test. Production never reassigns it.
var compactEntryFn = compactEntry

func compactEntry(ctx context.Context, entry *sessions.Entry, cfg *config.Config, model string, progress io.Writer) (*memory.CompactionResult, error) {
	model = compactionModelFor(cfg, model)
	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}

	sessionID := entry.SessionID
	var preloaded *agent.Session
	if sessionID == "" {
		switch {
		case entry.TranscriptPath != "":
			hist, herr := operations.HistoryForBackend(backendName)
			if herr != nil {
				return nil, fmt.Errorf("resolve history reader for backend %q: %w", backendName, herr)
			}
			preloaded, herr = hist.GetSessionByPath(entry.TranscriptPath)
			if herr != nil {
				return nil, fmt.Errorf("load session from transcript %q: %w", entry.TranscriptPath, herr)
			}
		case entry.CanonicalTranscriptPath != "":
			// Canonical fallback resolves the harp's own transcript by HarpName
			// inside the compactor; nothing to preload here.
		default:
			return nil, fmt.Errorf("harp %q has no session_id bound, no transcript path recorded, and no captured transcript; nothing to distill (the SessionStart bind hook records the ID for sessions launched via ctxloom run)", entry.HarpName)
		}
	}

	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:   cfg.GetCompactionLLM(),
		Model: model,
		// The compaction label is the FAST role (config.FastLabel), not the
		// primary — resolve its env from the same label the LLM above came
		// from, or the distiller gets a different backend's credentials.
		Env:              llmEnvFor(cfg, cfg.FastLabel()),
		Backend:          backendName,
		ChunkSize:        cfg.GetCompactionChunkSize(),
		SessionID:        sessionID,
		PreloadedSession: preloaded,
		WorkDir:          entry.ProjectDir,
		HarpName:         entry.HarpName,
		Progress:         progress,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}
	result, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("distillation failed: %w", err)
	}
	return result, nil
}

// resolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by loadOrDistillSession's callers (mcp_tools_memory.go) — and,
// before the deprecated `memory` command group was deleted, by
// `memory list`/`memory show` too. The legacy leg (pb.SessionReader) reads
// over gRPC to the agent server (self-situated, no workspace passed, works
// for a remote agent); it is wrapped in CanonicalFallbackSource so any harp
// with a captured canonical transcript is read from that instead — workDir
// scopes the canonical side to this project. A session-index open failure
// degrades to the legacy-only reader rather than failing the caller
// outright.
//
// A retired-scraper backend (codex/kiro/claude-code — their
// scrapers were deleted, not demoted) never gets a legacy
// leg at all: there is no plugin-side History() left to ask, so this never
// even spawns the plugin for that purpose. Every other backend (opencode's
// native reader included) keeps its legacy leg unchanged.
func resolveSessionSource(cfg *config.Config, backendName, workDir string) (pb.SessionSource, string, error) {
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}
	if !backends.Exists(backendName) {
		return nil, backendName, fmt.Errorf("unknown backend: %s", backendName)
	}
	var legacy pb.SessionSource
	if !pb.IsRetiredScraperBackend(backendName) {
		legacy = pb.NewSessionReader(backendName, 0)
	}
	store, err := sessions.Open("")
	if err != nil {
		clidiag.Warn("ctxloom", "session index open failed, reading legacy transcripts only: %v", err)
		if legacy != nil {
			return pb.NewFilteredSource(legacy, policy.Default()), backendName, nil
		}
		return nil, backendName, fmt.Errorf("session index unavailable and %s has no legacy transcript reader: %w", backendName, err)
	}
	// The content policy is applied HERE, at the one place a read source is
	// built, so every consumer that resolves a source through this function —
	// `session distill`, and the load/recover/get_previous MCP tools — sees
	// the same filtered view without each having to remember to wrap. The
	// wrap is on the READ side on purpose: what is on disk stays total, so
	// changing the policy changes what every existing transcript yields, with
	// no migration. See grpc.FilteredSource.
	return pb.NewFilteredSource(
		pb.NewCanonicalFallbackSource(legacy, workDir, store),
		policy.Default(),
	), backendName, nil
}

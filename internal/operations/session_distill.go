package operations

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript/policy"
)

// The distillation/compaction cluster the session commands share with the MCP
// memory tools: resolving the compaction model, running the compactor for one
// entry, and resolving a transcript source for a backend. CompactEntry is the
// single funnel every distill path goes through.

// CompactionModelFor resolves the model one distill runs with: an explicit
// caller override wins, and "" falls back to the configured compaction model.
// Kept as a named function despite the single call site (CompactEntry, which
// is itself the single funnel) because it is the one unit-testable statement
// of the override rule — honoring the override on only some distill paths
// is exactly the bug this exists to prevent — and CompactEntry needs a live
// session entry and compactor to exercise.
func CompactionModelFor(cfg *config.Config, override string) string {
	if override != "" {
		return override
	}
	return cfg.GetCompactionModel()
}

// DistillOptions carries what varies per distill invocation, as a struct
// rather than three more positional parameters: model and progress were
// already in flight and a third string argument beside them is where call
// sites start transposing them.
type DistillOptions struct {
	// Model overrides the compaction model for THIS call; "" uses
	// cfg.GetCompactionModel(). It exists so a caller-supplied model override
	// reaches the canonical/harp distill path too, not just the backend one.
	Model string
	// Progress receives human-readable distillation progress, or nil where the
	// caller has no safe sink for it (see memory.CompactionConfig.Progress).
	Progress io.Writer
	// PromptDir loads the distillation prompt from disk instead of the
	// embedded copy, for prompt evaluation. Empty uses the embedded prompt; a
	// prompt missing from the directory fails the distill rather than silently
	// falling back.
	PromptDir string
}

// CompactEntry runs the compactor for a single session entry and returns the
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
// IS reachable (GetSession resolved entry.TranscriptPath via
// fillTranscriptByLocation). Load by that path instead of failing; a canonical
// transcript (a oneshot Execute run's own transcript.jsonl, resolved by
// HarpName inside pb.NewCanonicalFallbackSource) needs no preload. Only
// hard-error when there is neither a bound id, a transcript path, nor a
// captured transcript — genuinely nothing to distill.
// opts carries the per-invocation knobs; its zero value is the ordinary
// config-driven distill.
//
// mcp's compactEntryFn is CompactEntry behind a package var so a caller's
// wiring can be observed in a test; that test seam stays in mcp and is not
// duplicated here.
func CompactEntry(ctx context.Context, entry *sessions.Entry, cfg *config.Config, opts DistillOptions) (*memory.CompactionResult, error) {
	model := CompactionModelFor(cfg, opts.Model)
	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}

	sessionID := entry.SessionID
	var preloaded *agent.Session
	if sessionID == "" {
		switch {
		case entry.TranscriptPath != "":
			hist, herr := HistoryForBackend(backendName)
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

	// What this session said it was about to do next, captured by the TurnEnd
	// hook while it was still live. The bool is discarded because there is
	// nothing else to do with "no hint": an absent hint IS the empty string,
	// and distillPrompt appends nothing for it.
	taskHint, _ := memory.ReadNextStep(entry.HarpName)
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		LLM:   cfg.GetCompactionLLM(),
		Model: model,
		// The compaction label is the FAST role (config.FastLabel), not the
		// primary — resolve its env from the same label the LLM above came
		// from, or the distiller gets a different backend's credentials.
		Env:              LLMEnvFor(cfg, cfg.FastLabel()),
		Backend:          backendName,
		EssenceMaxChars:  cfg.GetEssenceMaxChars(),
		SessionID:        sessionID,
		PreloadedSession: preloaded,
		WorkDir:          entry.ProjectDir,
		HarpName:         entry.HarpName,
		Progress:         opts.Progress,
		PromptDir:        opts.PromptDir,
		// What this session said it was about to do next, captured by the
		// TurnEnd hook while it was still live. Absent on a harp that has not
		// finished a turn, and absent is free: distillPrompt appends nothing.
		TaskHint: taskHint,
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

// ResolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by loadOrDistillSession's callers (cli's mcp_tools_memory.go) — and,
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
func ResolveSessionSource(cfg *config.Config, backendName, workDir string) (pb.SessionSource, string, error) {
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

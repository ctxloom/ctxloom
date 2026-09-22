package operations

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/policy"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// Hosts yields the coordinator the distilling one-shot runs on.
	Hosts RunHosts
	// Strictness is the posture the distilling one-shot launches under.
	Strictness strictness.Mode
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
	// The distiller is a real session on the FAST role's label: one harp for
	// every turn this compaction makes, started on the first turn and ended
	// when the compaction is done.
	distiller := NewLazyOneShot(opts.Hosts, cfg, opts.Strictness, cfg.FastLabel(), model, entry.ProjectDir, "", 0)
	defer distiller.End()
	// The compactor no longer builds its own source: resolve it here (unless a
	// transcript was preloaded by path, which short-circuits it) and inject.
	var source memory.Source
	if preloaded == nil {
		src, serr := distillSource(backendName, entry.ProjectDir)
		if serr != nil {
			return nil, fmt.Errorf("resolve transcript source for backend %q: %w", backendName, serr)
		}
		source = src
	}
	compactor, err := memory.NewCompactor(memory.CompactionConfig{
		Run:              distiller.Turn,
		Backend:          backendName,
		Source:           source,
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

// distillSource builds the transcript source the compactor reads for a
// distill: ctxloom's own canonical capture (transcript.CanonicalHistory via
// the canonical-fallback source), with the legacy per-engine scraper behind
// it only for a backend that still declares one — none of the shipped engines
// do. It is the resolution the compactor used to do inline before slice 14a
// moved source-building to the caller, minus the read-side content policy
// (a distill reads the raw transcript, the same bytes it always did). A
// session-index open failure degrades to the legacy-only reader, or errors
// when there is no legacy leg to fall back to.
// legacyTranscriptSource is the legacy leg of a transcript source: a reader
// over the engine's OWN store for an engine that still keeps one, scoped to
// workDir; nil (no error) for a retired-scraper engine, whose transcripts are
// canonical capture alone. As a transcript.Source interface value it is nil
// exactly when there is no leg — never a typed nil the fallback would
// dereference.
func legacyTranscriptSource(backend, workDir string) (transcript.Source, error) {
	if backends.NoLegacyHistoryReason(backend) != "" {
		return nil, nil
	}
	hist, err := HistoryForBackend(backend)
	if err != nil {
		return nil, err
	}
	return transcript.NewEngineReader(hist, workDir), nil
}

// DistillSource resolves the transcript source a compactor reads for a
// distill, for callers that build a memory.CompactionConfig directly (the MCP
// memory tools). It is distillSource behind an exported name so those callers
// need not know how a canonical source is assembled.
func DistillSource(backend, workDir string) (memory.Source, error) {
	return distillSource(backend, workDir)
}

func distillSource(backend, workDir string) (memory.Source, error) {
	legacy, err := legacyTranscriptSource(backend, workDir)
	if err != nil {
		return nil, err
	}
	store, err := sessions.Open(strictness.Sink("ctxloom"))
	switch {
	case err == nil:
		return transcript.NewCanonicalFallbackSource(legacy, workDir, store), nil
	case legacy != nil:
		return legacy, nil
	default:
		return nil, fmt.Errorf("session index unavailable and %s has no legacy transcript reader: %w", backend, err)
	}
}

// ResolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by loadOrDistillSession's callers (mcp's memory tools). The legacy
// leg (a transcript.EngineReader over the engine's own store, scoped to
// workDir) is wrapped in CanonicalFallbackSource so any harp with a captured
// canonical transcript is read from that instead — workDir scopes the
// canonical side to this project too. A session-index open failure degrades
// to the legacy-only reader rather than failing the caller outright.
//
// A retired-scraper backend (claude-code — its scraper was deleted, not
// demoted) never gets a legacy leg at all: there is no History() left to
// ask. Every other backend keeps its legacy leg unchanged.
func ResolveSessionSource(cfg *config.Config, backendName, workDir string) (transcript.Source, string, error) {
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}
	if !backends.Exists(backendName) {
		return nil, backendName, fmt.Errorf("unknown backend: %s", backendName)
	}
	legacy, err := legacyTranscriptSource(backendName, workDir)
	if err != nil {
		return nil, backendName, err
	}
	store, err := sessions.Open(strictness.Sink("ctxloom"))
	if err != nil {
		clidiag.Warn("ctxloom", "session index open failed, reading legacy transcripts only: %v", err)
		if legacy != nil {
			return transcript.NewFilteredSource(legacy, policy.Default()), backendName, nil
		}
		return nil, backendName, fmt.Errorf("session index unavailable and %s has no legacy transcript reader: %w", backendName, err)
	}
	// The content policy is applied HERE, at the one place a read source is
	// built, so every consumer that resolves a source through this function —
	// `session distill`, and the load/recover/get_previous MCP tools — sees
	// the same filtered view without each having to remember to wrap. The
	// wrap is on the READ side on purpose: what is on disk stays total, so
	// changing the policy changes what every existing transcript yields, with
	// no migration. See transcript.FilteredSource.
	return transcript.NewFilteredSource(
		transcript.NewCanonicalFallbackSource(legacy, workDir, store),
		policy.Default(),
	), backendName, nil
}

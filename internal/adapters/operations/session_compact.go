package operations

import (
	"context"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/core/engine"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"

	"github.com/ctxloom/ctxloom/internal/adapters/memory"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/policy"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// The compaction/compaction cluster the session commands share with the MCP
// memory tools: running the compactor for one entry, and resolving a
// transcript source for a backend. CompactEntry is the
// single funnel every compact path goes through.

// CompactOptions carries what varies per compact invocation, as a struct
// rather than three more positional parameters: model and progress were
// already in flight and a third string argument beside them is where call
// sites start transposing them.
type CompactOptions struct {
	// Hosts yields the coordinator the compacting one-shot runs on.
	Hosts RunHosts
	// Model overrides, for THIS call, the model of the label the distiller
	// runs on (DistillerOneShot); "" keeps that label's own. It exists so a
	// caller-supplied model override reaches the canonical/harp compact path
	// too, not just the backend one.
	Model string
	// Progress receives human-readable compaction progress, or nil where the
	// caller has no safe sink for it (see memory.CompactionConfig.Progress).
	Progress io.Writer
	// PromptDir loads the compaction prompts from disk instead of the
	// embedded copies, for prompt evaluation. Empty uses the embedded prompt; a
	// prompt missing from the directory fails the compact rather than silently
	// falling back.
	PromptDir string
}

// CompactEntry runs the compactor for a single session entry and returns the
// result. It does NOT change the working directory and does NOT load config —
// the caller supplies cfg and situates the process. Canonical-transcript
// sessions resolve cwd-independently through WorkDir, so the long-lived MCP
// server — which must never chdir — calls it as-is.
//
// session_id is recorded forward by the `ctxloom hook session-bind`
// SessionStart hook (see sessionBindCmd). A container-runtime harp's bind hook
// runs INSIDE the container, though, and the host session index is not mounted
// in — so its session_id never gets bound host-side. The unbound case is
// compactable's to settle: ctxloom's canonical capture is read by HarpName,
// and a harp with neither a bound id nor a capture has genuinely nothing to
// compact.
// opts carries the per-invocation knobs; its zero value is the ordinary
// config-driven compact.
//
// mcp's compactEntryFn is CompactEntry behind a package var so a caller's
// wiring can be observed in a test; that test seam stays in mcp and is not
// duplicated here.
func CompactEntry(ctx context.Context, f LaunchFacts, entry *sessions.Entry, cfg *config.Config, opts CompactOptions) (*memory.CompactionResult, error) {
	model := opts.Model
	backendName := entry.Backend
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}

	sessionID := entry.SessionID
	if err := compactable(entry); err != nil {
		return nil, err
	}

	// What this session said it was about to do next, captured by the TurnEnd
	// hook while it was still live. The bool is discarded because there is
	// nothing else to do with "no hint": an absent hint IS the empty string,
	// and compactPrompt appends nothing for it.
	taskHint, _ := memory.ReadNextStep(configFS(cfg), entry.HarpName)
	// The distiller is a real session run as the distiller agent: one harp
	// for every turn this compaction makes, started on the first turn and ended
	// when the compaction is done.
	distiller := DistillerOneShot(f, opts.Hosts, cfg).Model(model).WorkDir(entry.ProjectDir).Lazy()
	defer distiller.End()
	// The compactor does not build its own source: resolve it here and inject.
	source, err := CompactionSource(entry.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve transcript source for backend %q: %w", backendName, err)
	}
	compactor, err := memory.NewCompactor(configFS(cfg), memory.CompactionConfig{
		Run:             distiller.Turn,
		Backend:         backendName,
		Source:          source,
		EssenceMaxChars: cfg.GetEssenceMaxChars(),
		SessionID:       sessionID,
		WorkDir:         entry.ProjectDir,
		HarpName:        entry.HarpName,
		Progress:        opts.Progress,
		PromptDir:       opts.PromptDir,
		// What this session said it was about to do next, captured by the
		// TurnEnd hook while it was still live. Absent on a harp that has not
		// finished a turn, and absent is free: compactPrompt appends nothing.
		TaskHint: taskHint,
	})
	if err != nil {
		return nil, fmt.Errorf("create compactor: %w", err)
	}
	result, err := compactor.Compact(ctx)
	if err != nil {
		return nil, fmt.Errorf("compaction failed: %w", err)
	}
	return result, nil
}

// compactable reports whether the compactor has anything to resolve for
// entry: a bound session id, or ctxloom's canonical capture (read by
// HarpName). A recorded vendor transcript path alone is not readable: no
// shipped engine keeps a reader for its own store, so canonical capture is
// the only source.
func compactable(entry *sessions.Entry) error {
	if entry.SessionID != "" || entry.CanonicalTranscriptPath != "" {
		return nil
	}
	return fmt.Errorf("session %q has no session_id bound and no captured transcript; nothing to compact", entry.HarpName)
}

// CompactionSource builds the transcript source the compactor reads for a
// compact: ctxloom's own canonical capture, scoped to workDir, read raw (no
// read-side content policy — a compact reads the transcript's own bytes).
// Callers that build a memory.CompactionConfig directly (the MCP memory
// tools) use it so they need not know how a canonical source is assembled.
func CompactionSource(workDir string) (memory.Source, error) {
	store, err := sessions.Open(strictness.Sink("ctxloom"))
	if err != nil {
		return nil, fmt.Errorf("session index unavailable: %w", err)
	}
	return transcript.NewCanonicalFallbackSource(workDir, store), nil
}

// ResolveSessionSource resolves the backend (defaulting when empty) and a
// transcript source for it, returning the resolved backend name for display.
// Shared by loadOrCompactSession's callers (mcp's memory tools). The source
// is ctxloom's canonical capture, scoped to workDir; a session-index open
// failure is the caller's error, since there is no other source to read.
func ResolveSessionSource(reg engine.Registry, cfg *config.Config, backendName, workDir string) (transcript.Source, string, error) {
	if backendName == "" {
		backendName = cfg.GetDefaultLLM()
	}
	if !EngineExists(reg, backendName) {
		return nil, backendName, fmt.Errorf("unknown backend: %s", backendName)
	}
	store, err := sessions.Open(strictness.Sink("ctxloom"))
	if err != nil {
		return nil, backendName, fmt.Errorf("session index unavailable: %w", err)
	}
	// The content policy is applied HERE, at the one place a read source is
	// built, so every consumer that resolves a source through this function —
	// `session compact`, and the load/recover/get_previous MCP tools — sees
	// the same filtered view without each having to remember to wrap. The
	// wrap is on the READ side on purpose: what is on disk stays total, so
	// changing the policy changes what every existing transcript yields, with
	// no migration. See transcript.FilteredSource.
	return transcript.NewFilteredSource(
		transcript.NewCanonicalFallbackSource(workDir, store),
		policy.Default(),
	), backendName, nil
}

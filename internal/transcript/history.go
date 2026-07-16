// This file (history.go) defines the READER counterpart to record.go/
// recorder.go: transcript.CanonicalHistory turns a harp's captured
// transcript.acp.jsonl (paths.HarpCanonicalTranscriptPath) back into the
// normalized agent.Session/agent.SessionMeta shape ctxloom's memory
// consumers already depend on (tough-cloud plan §4a, slice S3).
//
// CanonicalHistory is engine-agnostic by construction: every structured
// engine's frames already folded through internal/acp/mapping.go before the
// Recorder (recorder.go) ever wrote a line, so there is exactly one reader
// for codex/kiro/claude/opencode/acp/antigravity instead of the four
// per-engine scrapers this package supersedes (internal/claude,
// internal/codex, internal/kiro, internal/antigravity — retirement is S5,
// not this slice).
//
// Deliberately NOT imported here: internal/lm/grpc (aliased `pb` elsewhere),
// whose pb.SessionSource interface this type's method set structurally
// satisfies (GetSession/ListSessions/CurrentSession, matching signatures
// exactly — see history_interface_test.go's external black-box assertion).
// S2 (running in parallel on this same release) wires internal/lm/grpc/
// chat.go to import THIS package for Tee; importing pb back from here would
// create transcript -> grpc -> transcript, an import cycle. A consumer
// package (S4) declares the `var _ pb.SessionSource = (*CanonicalHistory)(nil)`
// assertion when it wires CanonicalHistory in.
package transcript

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// CanonicalHistory is the harp-keyed, project-scoped read view over ctxloom's
// own captured transcripts. Unlike the per-engine SessionHistory
// implementations it supersedes — which need workDir on every call to locate
// a backend-native session-store directory before they can even resolve a
// sessionID — a harp alone is enough to resolve
// paths.HarpCanonicalTranscriptPath, so GetSession takes no workDir at all.
// workDir is still needed to scope ListSessions/CurrentSession to "this
// project's sessions" (there is no per-project canonical directory; harps
// live in one flat ~/.ctxloom/sessions/<harp>/ root), resolved via the
// harp<->project index (internal/sessions.Store).
type CanonicalHistory struct {
	workDir string
	store   sessions.Store
}

// NewCanonicalHistory returns a CanonicalHistory scoped to workDir, resolving
// project membership through store (the harp<->project session index;
// *sessions.Manager in production, *sessions.MemStore in tests).
func NewCanonicalHistory(workDir string, store sessions.Store) *CanonicalHistory {
	return &CanonicalHistory{workDir: workDir, store: store}
}

// GetSession materializes harpName's canonical transcript. harpName IS the
// lookup key — there is no separate backend-native session id to resolve, the
// exact simplification the canonical schema buys (every line already carries
// the harp). Returns an error, not an empty Session, when no canonical
// transcript was ever captured for this harp: the mirror image of the
// Recorder's empty-input contract (NewRecorder leaves no file for a chat that
// produced zero events, recorder.go), so "no file" and "captured, empty"
// never get confused on the read side either.
func (h *CanonicalHistory) GetSession(_ context.Context, harpName string) (*agent.Session, error) {
	if harpName == "" {
		return nil, fmt.Errorf("transcript: GetSession requires a non-empty harp")
	}
	path, err := paths.HarpCanonicalTranscriptPath(harpName)
	if err != nil {
		return nil, fmt.Errorf("transcript: resolve canonical transcript path for harp %q: %w", harpName, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, fmt.Errorf("transcript: no canonical transcript captured for harp %q", harpName)
		}
		return nil, fmt.Errorf("transcript: stat %s: %w", path, statErr)
	}
	return ParseTranscriptFile(path, harpName)
}

// ListSessions returns SessionMeta for every harp in this project that has a
// canonical transcript, most-recent-first — the same ordering
// sessions.Store.ListForProject already produces (activity-time, not
// creation-time). A harp with NO canonical transcript (pre-capture,
// interactive-pty-only, or a zero-event chat) is simply absent from the
// result; S3 does not fall back to a legacy per-engine reader for it (that
// selection is S4's job, plan §4b "Selection rule").
//
// A harp whose canonical file exists but fails to parse (corrupt beyond
// recovery, or an evolved schema version this build doesn't know — see
// ParseTranscriptFile) is skipped with a warning rather than failing the
// whole listing: one bad session must not hide every other project session
// from `session list`/the resume picker. GetSession, by contrast, is a
// direct request for exactly that harp and surfaces the same failure as a
// hard error — see its doc comment.
func (h *CanonicalHistory) ListSessions(_ context.Context) ([]agent.SessionMeta, error) {
	entries, err := h.store.ListForProject(h.workDir)
	if err != nil {
		return nil, fmt.Errorf("transcript: list sessions for %s: %w", h.workDir, err)
	}

	var metas []agent.SessionMeta
	for _, e := range entries {
		if e.CanonicalTranscriptPath == "" {
			continue
		}
		sess, perr := ParseTranscriptFile(e.CanonicalTranscriptPath, e.HarpName)
		if perr != nil {
			clidiag.Warn("ctxloom", "transcript: skip %s (%s): %v", e.HarpName, e.CanonicalTranscriptPath, perr)
			continue
		}
		metas = append(metas, agent.SessionMeta{
			ID:         e.HarpName,
			StartTime:  sess.StartTime,
			EndTime:    sess.EndTime,
			EntryCount: len(sess.Entries),
			Path:       e.CanonicalTranscriptPath,
		})
	}
	return metas, nil
}

// CurrentSession returns the most-recently-active canonical-backed session
// for this project, or (nil, nil) when none exists — the same "clean no
// sessions" contract pb.SessionReader.CurrentSession documents, so a caller
// can present an empty state without special-casing this reader.
func (h *CanonicalHistory) CurrentSession(ctx context.Context) (*agent.Session, error) {
	entries, err := h.store.ListForProject(h.workDir)
	if err != nil {
		return nil, fmt.Errorf("transcript: current session for %s: %w", h.workDir, err)
	}
	for _, e := range entries { // already most-recent-first
		if e.CanonicalTranscriptPath == "" {
			continue
		}
		return h.GetSession(ctx, e.HarpName)
	}
	return nil, nil
}

// ParseTranscriptFile reads a canonical transcript.acp.jsonl at path and
// reconstructs the agent.Session it represents: KindEntry lines fold into
// Session.Entries verbatim (callers wanting only the main thread — memory
// distillation, session-load replay — filter via agent.MainThreadEntries,
// backend.go:171); KindSession/KindComplete/KindPermission lines are envelope
// metadata, not conversation turns, so they do NOT become entries, but their
// ts still contributes to the returned StartTime/EndTime span (a session
// commonly opens on a KindSession header before the first entry and closes on
// a KindComplete accounting line after the last one — using every line's ts,
// not just entries', keeps the span honest). id becomes Session.ID.
//
// Two distinct failure modes, per plan §5/§6.4:
//   - A line that fails to parse as JSON at all (truncated mid-write, disk
//     corruption) is DROPPED, not fatal — the reader degrades to a valid
//     partial transcript from everything readable before the break, the same
//     contract agent.SessionStore.ParseSessionFile already guarantees for
//     every per-engine reader.
//   - A line that parses but carries a schema version this build does not
//     recognize (Record.V != SchemaVersion) is a HARD error for the whole
//     file: silently re-interpreting an evolved shape under the old rules is
//     exactly the silent-mis-parse failure mode the versioned envelope exists
//     to prevent ("isolation-must-not-negotiate": fail loud, never guess).
func ParseTranscriptFile(path, id string) (*agent.Session, error) {
	store := agent.NewSessionStore()

	var (
		start, end time.Time
		versionErr error
	)
	sess, err := store.ParseSessionFile(path, id, func(line []byte) []agent.SessionEntry {
		if versionErr != nil {
			return nil // already fatal; stop contributing further lines
		}
		var rec Record
		if jsonErr := json.Unmarshal(line, &rec); jsonErr != nil {
			return nil // corrupt/truncated line: degrade to partial
		}
		if rec.V != SchemaVersion {
			versionErr = fmt.Errorf("transcript: %s: line carries schema version %d, this reader knows version %d — refusing to guess at an unrecognized shape", path, rec.V, SchemaVersion)
			return nil
		}
		if start.IsZero() || rec.TS.Before(start) {
			start = rec.TS
		}
		if rec.TS.After(end) {
			end = rec.TS
		}
		return entriesFromRecord(rec)
	})
	if err != nil {
		return nil, fmt.Errorf("transcript: parse %s: %w", path, err)
	}
	if versionErr != nil {
		return nil, versionErr
	}

	if !start.IsZero() {
		sess.StartTime = start
		sess.EndTime = end
	}
	return sess, nil
}

// entriesFromRecord converts one already-version-checked Record into zero or
// one agent.SessionEntry: KindEntry records carry exactly one; every other
// Kind (session/complete/permission) is envelope metadata and contributes no
// entry.
func entriesFromRecord(rec Record) []agent.SessionEntry {
	if rec.Kind != KindEntry || rec.Entry == nil {
		return nil
	}
	return []agent.SessionEntry{{
		Timestamp:  rec.TS,
		Type:       agent.SessionEntryType(rec.Entry.Type),
		Content:    rec.Entry.Content,
		ToolName:   rec.Entry.ToolName,
		ToolInput:  rec.Entry.ToolInput,
		ToolOutput: rec.Entry.ToolOutput,
		IsError:    rec.Entry.IsError,
		Sidechain:  rec.Entry.Sidechain,
	}}
}

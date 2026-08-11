// This file (history.go) defines the READER counterpart to record.go/
// recorder.go: transcript.CanonicalHistory turns a harp's captured
// transcript.jsonl (paths.HarpCanonicalTranscriptPath, resolved for reads via
// paths.ResolveHarpCanonicalTranscriptPath so a pre-rename
// transcript.acp.jsonl still resolves) back into the normalized
// agent.Session/agent.SessionMeta shape ctxloom's memory consumers already
// depend on (slice S3).
//
// CanonicalHistory is engine-agnostic by construction: every structured
// engine's frames already folded through internal/acp/mapping.go before the
// Recorder (recorder.go) ever wrote a line, so there is exactly one reader
// for codex/kiro/claude/opencode/acp instead of the three
// per-engine scrapers this package supersedes (internal/claude,
// internal/codex, internal/kiro — retirement is S5,
// not this slice). It also still reads canonical transcripts captured under
// backend=antigravity before that engine was removed in 0.7.0 — the reader
// makes no assumption that its engine name is currently registered.
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
	"errors"
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

// NoCanonicalTranscriptError reports that harpName has no captured canonical
// transcript on disk — GetSession's os.IsNotExist case. It is a distinct type
// (not a bare fmt.Errorf) so a caller several layers up (e.g. the
// recover_session MCP handler) can errors.As it to extract the harp and
// produce an actionable message, rather than pattern-matching the error text.
// Error() keeps the original wording so existing callers that only check for
// "an error" (never the text) see no behavior change.
type NoCanonicalTranscriptError struct {
	Harp string
}

func (e *NoCanonicalTranscriptError) Error() string {
	return fmt.Sprintf("transcript: no canonical transcript captured for harp %q", e.Harp)
}

// SchemaVersionError reports that a canonical transcript line carries a
// schema version this build does not recognize. It is a distinct type (not a
// bare fmt.Errorf) for the same reason NoCanonicalTranscriptError is: every
// consumer of ParseTranscriptFile reduces failure to `err != nil` and then
// handles it exactly like a corrupt file — skipped from a listing, degraded
// to live-only, retried on the next poll tick. Those responses are defensible
// for damage; they are actively misleading for version skew, which is not
// damage at all, will never resolve by retrying, and has one obvious remedy.
// Telling the two apart has to be possible before any consumer can act on the
// difference.
type SchemaVersionError struct {
	Path  string
	Found int // the version the file carries
	Known int // the version this build reads
}

func (e *SchemaVersionError) Error() string {
	return fmt.Sprintf("transcript: %s: line carries schema version %d, this reader knows version %d — refusing to guess at an unrecognized shape", e.Path, e.Found, e.Known)
}

// Newer reports whether the file was written by a LATER build than this one,
// which is the case with an actionable remedy: upgrade. The reverse (a file
// older than any version this build still reads) is a different situation and
// must not be reported with the same advice.
func (e *SchemaVersionError) Newer() bool { return e.Found > e.Known }

// GetSession materializes harpName's canonical transcript. harpName IS the
// lookup key — there is no separate backend-native session id to resolve, the
// exact simplification the canonical schema buys (every line already carries
// the harp). Returns an error, not an empty Session, when no canonical
// transcript was ever captured for this harp: the mirror image of the
// Recorder's empty-input contract (NewRecorder leaves no file for a chat that
// produced zero events, recorder.go), so "no file" and "captured, empty"
// never get confused on the read side either — and nor does
// "a file that decoded to nothing", which ParseTranscriptFile now also
// errors on rather than returning as an empty conversation.
func (h *CanonicalHistory) GetSession(_ context.Context, harpName string) (*agent.Session, error) {
	if harpName == "" {
		return nil, fmt.Errorf("transcript: GetSession requires a non-empty harp")
	}
	path, err := paths.ResolveHarpCanonicalTranscriptPath(harpName)
	if err != nil {
		return nil, fmt.Errorf("transcript: resolve canonical transcript path for harp %q: %w", harpName, err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, &NoCanonicalTranscriptError{Harp: harpName}
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
			warnSkippedTranscript(e.HarpName, e.CanonicalTranscriptPath, perr)
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
//
// A candidate that cannot be read is SKIPPED with a warning and the next
// most-recent one tried, exactly as ListSessions does: the two must survive
// the same store state, or one stale entry answers "no current session" for a
// project whose sessions `session list` is happily enumerating. Only when
// every candidate fails is an error returned — and then it is the first
// failure, not (nil, nil), because "none of these files could be read" and
// "this project has no sessions" are different facts and the caller
// (lm/grpc's CanonicalFallbackSource) routes on the difference. See
// ParseTranscriptFile's doc for the same discrimination on the read side.
func (h *CanonicalHistory) CurrentSession(ctx context.Context) (*agent.Session, error) {
	entries, err := h.store.ListForProject(h.workDir)
	if err != nil {
		return nil, fmt.Errorf("transcript: current session for %s: %w", h.workDir, err)
	}
	var firstErr error
	for _, e := range entries { // already most-recent-first
		if e.CanonicalTranscriptPath == "" {
			continue
		}
		sess, serr := h.GetSession(ctx, e.HarpName)
		if serr == nil {
			return sess, nil
		}
		if firstErr == nil {
			firstErr = serr
		}
		warnSkippedTranscript(e.HarpName, e.CanonicalTranscriptPath, serr)
	}
	// Every candidate failed. That is NOT the clean "no sessions" state: the
	// project has captured transcripts, none of them could be read, and
	// reporting (nil, nil) would assert the conversations were empty when all
	// that is known is that the files were unreadable — the discrimination
	// ParseTranscriptFile's doc states and lm/grpc's fallback source routes on.
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, nil
}

// warnSkippedTranscript reports one transcript this reader passed over, and
// is the single place the version case is separated from every other failure.
// Both are skipped identically — one bad session must not hide the rest — but
// they are not the same news: "this file is damaged" invites the user to
// write the session off, while "this file was written by a newer ctxloom"
// means the session is intact and one upgrade away.
func warnSkippedTranscript(harp, path string, err error) {
	var vErr *SchemaVersionError
	if errors.As(err, &vErr) && vErr.Newer() {
		clidiag.Warn("ctxloom", "transcript: skip %s (%s): written by a newer ctxloom (schema v%d; this build reads v%d) — the session is intact, upgrade ctxloom to read it", harp, path, vErr.Found, vErr.Known)
		return
	}
	clidiag.Warn("ctxloom", "transcript: skip %s (%s): %v", harp, path, err)
}

// ParseTranscriptFile reads a canonical transcript.jsonl at path and
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
//   - A file from which NOT ONE record decoded — every line corrupt, or zero
//     bytes — is a HARD error too. Degrading to partial only means
//     anything when there IS a part; "the whole file was unreadable" and "the
//     conversation was empty" are different facts, and returning an empty
//     Session with a nil error asserted the second when only the first was
//     known. It also suppressed lm/grpc/canonical_source.go's legacy fallback,
//     which runs only when this errors. Note the discrimination that matters:
//     zero ENTRIES is legitimate (a file of session/complete envelope lines is
//     a real, entry-less conversation and returns nil); zero RECORDS is not,
//     because the writer creates the file lazily on the first SUCCESSFUL
//     Record (see NewRecorder), so it cannot legitimately decode to nothing.
func ParseTranscriptFile(path, id string) (*agent.Session, error) {
	// agent.NewSessionStore() used to be the OS-filesystem constructor here,
	// but a zero-value SessionStore{} is identical: every FS use goes through
	// agent.GetFS, which already defaults a nil Fs to afero.NewOsFs()
	// (the redundant constructor was deleted).
	store := agent.SessionStore{}

	var (
		lines, decoded int
		start, end     time.Time
		versionErr     error
	)
	sess, err := store.ParseSessionFile(path, id, func(line []byte) []agent.SessionEntry {
		lines++
		if versionErr != nil {
			return nil // already fatal; stop contributing further lines
		}
		var rec Record
		if jsonErr := json.Unmarshal(line, &rec); jsonErr != nil {
			return nil // corrupt/truncated line: degrade to partial
		}
		if rec.V != SchemaVersion {
			versionErr = &SchemaVersionError{Path: path, Found: rec.V, Known: SchemaVersion}
			return nil
		}
		if start.IsZero() || rec.TS.Before(start) {
			start = rec.TS
		}
		if rec.TS.After(end) {
			end = rec.TS
		}
		decoded++
		return entriesFromRecord(rec)
	})
	if err != nil {
		return nil, fmt.Errorf("transcript: parse %s: %w", path, err)
	}
	if versionErr != nil {
		return nil, versionErr
	}
	if decoded == 0 {
		if lines == 0 {
			return nil, fmt.Errorf("transcript: %s: file carries no lines at all — the writer creates a canonical transcript only on the first SUCCESSFULLY recorded event, so a zero-byte one is a failed or truncated capture, not an empty conversation", path)
		}
		return nil, fmt.Errorf("transcript: %s: read %d line(s) but decoded 0 canonical records — the file exists yet nothing in it is readable as a transcript (corrupted or foreign capture); refusing to report this as an empty conversation", path, lines)
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
	e := rec.Entry
	return []agent.SessionEntry{{
		Timestamp:     rec.TS,
		Type:          agent.SessionEntryType(e.Type),
		Content:       e.Content,
		ToolName:      e.ToolName,
		ToolInput:     e.ToolInput,
		ToolOutput:    e.ToolOutput,
		IsError:       e.IsError,
		Sidechain:     e.Sidechain,
		ToolCallID:    e.ToolCallID,
		ToolKind:      e.ToolKind,
		ToolLocations: toolLocationsFromPayload(e.ToolLocations),
		ToolContent:   toolContentFromPayload(e.ToolContent),
		ContentBlocks: contentBlocksFromPayload(e.ContentBlocks),
		SystemKind:    agent.SessionSystemKind(e.SystemKind),
		Plan:          planEntriesFromPayload(e.Plan),
	}}
}

func toolLocationsFromPayload(locs []ToolLocation) []agent.ToolLocation {
	if len(locs) == 0 {
		return nil
	}
	out := make([]agent.ToolLocation, 0, len(locs))
	for _, l := range locs {
		out = append(out, agent.ToolLocation{Path: l.Path, Line: l.Line})
	}
	return out
}

func contentBlocksFromPayload(blocks []ContentBlock) []agent.ContentBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]agent.ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, agent.ContentBlock{Kind: b.Kind, Text: b.Text, Raw: b.Raw})
	}
	return out
}

func toolContentFromPayload(content []ToolContentBlock) []agent.ToolContentBlock {
	if len(content) == 0 {
		return nil
	}
	out := make([]agent.ToolContentBlock, 0, len(content))
	for _, c := range content {
		out = append(out, agent.ToolContentBlock{
			Kind: c.Kind, Text: c.Text,
			DiffPath: c.DiffPath, DiffOldText: c.DiffOldText, DiffNewText: c.DiffNewText,
			TerminalID: c.TerminalID, Raw: c.Raw,
		})
	}
	return out
}

func planEntriesFromPayload(entries []PlanEntry) []agent.PlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]agent.PlanEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, agent.PlanEntry{Content: e.Content, Priority: e.Priority, Status: e.Status})
	}
	return out
}

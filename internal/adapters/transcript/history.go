// This file (history.go) defines the READER counterpart to record.go/
// recorder.go: ParseTranscriptFile turns a harp's captured transcript.jsonl
// (paths.HarpCanonicalTranscriptPath) back into the normalized agent.Session
// shape.
//
// It is engine-agnostic by construction: every structured engine's frames
// already folded onto agent.ChatEvent before the Recorder (recorder.go) ever
// wrote a line, so there is exactly one reader for every structured engine.
// It makes no assumption that a transcript's engine name is currently
// registered: a canonical transcript captured under a since-removed engine
// still reads.
package transcript

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// SchemaVersionError reports that a canonical transcript line carries a
// schema version this build does not recognize. It is a distinct type (not a
// bare fmt.Errorf) so a caller can tell it apart: every
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

// ParseTranscriptFile reads a canonical transcript.jsonl at path and
// reconstructs the agent.Session it represents: KindEntry lines fold into
// Session.Entries verbatim (callers wanting only the main thread — memory
// distillation, session-load replay — filter via agent.MainThreadEntries,
// backend.go:171); KindSession/KindComplete/KindDenied lines are envelope
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
//     known. It also suppressed the canonical-fallback source's legacy fallback,
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

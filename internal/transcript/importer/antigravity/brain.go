package antigravity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

// step is one line of a transcript_full.jsonl file — antigravity's own
// step-log record, per docs/transcript-schema.md §2b: {step_index, source:
// USER_EXPLICIT|SYSTEM|MODEL, type: USER_INPUT|CONVERSATION_HISTORY|
// PLANNER_RESPONSE|..., content, created_at, status}. Only Type, Status, and
// Content are read here; step_index/source/created_at carry nothing this
// adapter needs (Type alone discriminates what to do with a line; ordering
// is already the file's own line order, which convertLines preserves without
// consulting step_index). Unlike codex's rolloutLine, this is NOT a wrapper
// envelope around a nested payload — every field this adapter cares about is
// already at the top level, so one struct suffices where codex needed two
// (rolloutLine + a payload type per variant).
type step struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Content string `json:"content"`
}

// stepDone is the only status value this adapter has ever observed on a
// USER_INPUT or PLANNER_RESPONSE step across every real transcript_full.jsonl
// file available on this box (see testdata/MANIFEST.json) — every other
// status seen at all (RUNNING) belongs to a step type Convert never
// converts anyway (RUN_COMMAND). Gating on it regardless is defensive, not
// redundant: a still-RUNNING or errored USER_INPUT/PLANNER_RESPONSE would be
// provisional content, not the finalized turn this adapter's canonical
// output promises.
const stepDone = "DONE"

// userRequestOpenTag / userRequestCloseTag bracket the human-authored part of
// a USER_INPUT step's content. antigravity wraps every user turn as
// "<USER_REQUEST>...</USER_REQUEST><ADDITIONAL_METADATA>...</ADDITIONAL_METADATA>
// <USER_SETTINGS_CHANGE>...</USER_SETTINGS_CHANGE>" (verified: every USER_INPUT
// step on this box carries this exact wrapper) — ADDITIONAL_METADATA (a
// system-stamped local-time note) and USER_SETTINGS_CHANGE (a system-stamped
// settings-change notice) are ctxloom-irrelevant framing the human never
// typed, the same reasoning codex's messageEvents uses to skip role=developer
// response_items. Recording the wrapper verbatim would misrepresent system
// scaffolding as something the user wrote.
const (
	userRequestOpenTag  = "<USER_REQUEST>"
	userRequestCloseTag = "</USER_REQUEST>"
)

// convertLines runs a single streamed pass over lines in the file's own
// order. Unlike codex, there is no separate scanSessionInfo pass: antigravity's
// native format carries no model/permission-mode/context-window/session-id
// field anywhere (docs/transcript-schema.md §2c lists "turn accounting" as
// "(none)" for antigravity-native, and no field in `step` corresponds to
// ChatSessionInfo at all) — so Convert never emits a KindSession or
// KindComplete line, matching the existing hand-captured oneshot-regime
// fixture (internal/transcript/testdata/fixtures/antigravity.transcript.acp.jsonl),
// which is entry-only too.
func convertLines(ctx context.Context, rec transcript.Recorder, lines [][]byte) error {
	record := importer.RecordFunc(rec, "antigravity")
	var (
		lineCount   int
		malformed   int
		convertible int // lines whose Type is one this adapter maps at all
		wrongStatus int // convertible lines skipped only for Status != stepDone
		entries     int
	)
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		lineCount++
		var s step
		if err := json.Unmarshal(line, &s); err != nil {
			malformed++
			continue // malformed line: skip, never fatal (see importer.VendorAdapter doc)
		}
		if s.Type != "USER_INPUT" && s.Type != "PLANNER_RESPONSE" {
			continue // administrative/unmapped step type: not this adapter's content
		}
		convertible++
		if s.Status != stepDone {
			wrongStatus++
			continue // provisional (e.g. RUNNING): not a finalized turn to record
		}
		for _, ev := range stepEvent(s) {
			if err := record(ev); err != nil {
				return err
			}
			entries++
		}
	}
	// U146-F01/F02: a file that decodes fine but yields zero entries is
	// indistinguishable, without this check, from a genuinely empty or
	// admin-only conversation — and because transcript.Recorder only creates
	// its file on the first SUCCESSFUL Record (recorder.go's NewRecorder doc),
	// nothing on disk would ever mark the failure either: the same drifted
	// file would report "success" again on every future retry. Fail loud only
	// when there was real convertible content to have converted; an
	// admin-only file (convertible == 0) stays a legitimate, silent success.
	if entries == 0 && convertible > 0 {
		if wrongStatus == convertible {
			return fmt.Errorf("antigravity: read %d line(s) including %d USER_INPUT/PLANNER_RESPONSE step(s), but none had status %q — this build's status vocabulary no longer matches the file", lineCount, convertible, stepDone)
		}
		return fmt.Errorf("antigravity: read %d line(s) including %d USER_INPUT/PLANNER_RESPONSE step(s) but converted ZERO transcript entries — the vendor format this build parses no longer matches the file", lineCount, convertible)
	}
	if entries == 0 && malformed > 0 && malformed == lineCount {
		return fmt.Errorf("antigravity: all %d line(s) failed to parse as JSON — not a transcript this build can read", lineCount)
	}
	return nil
}

// stepEvent maps one DONE step to zero or one ChatEvents, per
// docs/transcript-schema.md §2c's mapping table: USER_INPUT -> "user",
// PLANNER_RESPONSE -> "assistant". Every other Type (CONVERSATION_HISTORY,
// CHECKPOINT, GENERIC, LIST_DIRECTORY, RUN_COMMAND, CODE_ACTION, VIEW_FILE,
// SYSTEM_MESSAGE, ERROR_MESSAGE, and any future/unrecognized type) is
// skipped — see this package's doc comment for why that vocabulary is not
// converted even though some of it is real, present data on this box.
func stepEvent(s step) []agent.ChatEvent {
	switch s.Type {
	case "USER_INPUT":
		return importer.TextEntry(agent.EntryTypeUser, extractUserRequest(s.Content))
	case "PLANNER_RESPONSE":
		return importer.TextEntry(agent.EntryTypeAssistant, strings.TrimSpace(s.Content))
	default:
		return nil
	}
}

// extractUserRequest pulls the text between <USER_REQUEST> and
// </USER_REQUEST> out of a USER_INPUT step's content (see
// userRequestOpenTag's doc comment for why the rest of the wrapper is
// dropped). Falls back to the full trimmed content when the wrapper is
// absent — defensive for a hypothetical antigravity build that stops
// wrapping user turns, so a format change degrades to "record the whole
// thing" rather than to "record nothing."
func extractUserRequest(content string) string {
	start := strings.Index(content, userRequestOpenTag)
	if start == -1 {
		return strings.TrimSpace(content)
	}
	start += len(userRequestOpenTag)
	end := strings.Index(content[start:], userRequestCloseTag)
	if end == -1 {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(content[start : start+end])
}

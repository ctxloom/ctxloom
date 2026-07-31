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
//
// Content is json.RawMessage, not string, so that a `content` field which is
// still valid JSON but no longer a STRING is a fact this adapter can observe
// and report — see stepText. Decoding it as a string made vendor shape drift
// indistinguishable from byte corruption: json.Unmarshal failed on the whole
// line, and the line was discarded through the identical path a truncated,
// genuinely corrupt line takes, so a file whose every line parses fine was
// reported as "failed to parse as JSON".
type step struct {
	Type    string          `json:"type"`
	Status  string          `json:"status"`
	Content json.RawMessage `json:"content"`
}

// stepText decodes a step's `content` field. antigravity has only ever emitted
// it as a bare JSON string on this box (see testdata/MANIFEST.json), and an
// absent or null field is a legitimate empty content, not a problem.
//
// ok=false means the field is PRESENT and is valid JSON but is no longer a
// string — the vendor's shape has drifted. That is a different failure from a
// line whose bytes are not JSON at all: the step's own type and status are
// still perfectly readable, so the adapter still knows a turn was there and
// can say what it could not read. Reporting it as malformed would name the
// wrong cause and, on a file where every line drifted, would claim the file
// is not JSON when every byte of it is.
func stepText(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
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

// stepTypeUser / stepTypeAssistant / stepTypeError are the step types this
// adapter converts. The first two are docs/transcript-schema.md §2c's mapping
// table verbatim; stepTypeError is antigravity's own record that something in
// the session FAILED, which the canonical vocabulary already has a slot for
// (entry.type "system", SystemKindNotice) — see stepEvent.
const (
	stepTypeUser      = "USER_INPUT"
	stepTypeAssistant = "PLANNER_RESPONSE"
	stepTypeError     = "ERROR_MESSAGE"
)

// convertibleStep reports whether this adapter maps s.Type at all, and
// whether s is finalized enough to convert. An ERROR_MESSAGE is exempt from
// the stepDone gate ON PURPOSE: no ERROR_MESSAGE step exists in any capture on
// this box, so the status an errored step carries has never been observed, and
// gating a failure notice behind an unverified status vocabulary would drop
// the one record that says the session went wrong.
func convertibleStep(s step) (mapped, finalized bool) {
	switch s.Type {
	case stepTypeError:
		return true, true
	case stepTypeUser, stepTypeAssistant:
		return true, s.Status == stepDone
	default:
		return false, false
	}
}

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
		drifted     int // convertible lines whose `content` is no longer a string
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
		mapped, finalized := convertibleStep(s)
		if !mapped {
			continue // administrative/unmapped step type: not this adapter's content
		}
		convertible++
		if !finalized {
			wrongStatus++
			continue // provisional (e.g. RUNNING): not a finalized turn to record
		}
		text, readable := stepText(s.Content)
		if !readable {
			drifted++
			continue // `content` is valid JSON but no longer a string: see stepText
		}
		for _, ev := range stepEvent(s.Type, text) {
			if err := record(ev); err != nil {
				return err
			}
			entries++
		}
	}
	if drifted > 0 {
		agent.Warn("antigravity transcript import: %d step(s) carried a `content` field that is no longer a JSON string — this build cannot read that shape, and their text was dropped", drifted)
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
		if drifted == convertible {
			return fmt.Errorf("antigravity: read %d line(s) including %d step(s) of a type this adapter maps, but every one carried a `content` field that is no longer a JSON string — the lines are valid JSON, this build's content shape is what no longer matches", lineCount, convertible)
		}
		if wrongStatus == convertible {
			return fmt.Errorf("antigravity: read %d line(s) including %d step(s) of a type this adapter maps, but none had status %q — this build's status vocabulary no longer matches the file", lineCount, convertible, stepDone)
		}
		return fmt.Errorf("antigravity: read %d line(s) including %d step(s) of a type this adapter maps but converted ZERO transcript entries — the vendor format this build parses no longer matches the file", lineCount, convertible)
	}
	if entries == 0 && malformed > 0 && malformed == lineCount {
		return fmt.Errorf("antigravity: all %d line(s) failed to parse as JSON — not a transcript this build can read", lineCount)
	}
	return nil
}

// stepEvent maps one convertible step to zero or one ChatEvents, per
// docs/transcript-schema.md §2c's mapping table: USER_INPUT -> "user",
// PLANNER_RESPONSE -> "assistant", ERROR_MESSAGE -> "system".
//
// The ERROR_MESSAGE row records a session that FAILED. It is not a fidelity
// bonus: without it, a failed antigravity session imports as a clean
// transcript whose only evidence of the failure is content that is no longer
// there — indistinguishable from a session that simply ended. No vocabulary is
// invented for it, which is the bar this package's doc comment sets for
// everything it declines to map: agent.EntryTypeSystem's default
// SystemKindNotice is documented as "a freeform system notice with no
// structured payload," which is exactly what an ERROR_MESSAGE's content is.
//
// Every other Type (CONVERSATION_HISTORY, CHECKPOINT, GENERIC, LIST_DIRECTORY,
// RUN_COMMAND, CODE_ACTION, VIEW_FILE, SYSTEM_MESSAGE, and any
// future/unrecognized type) is skipped — see this package's doc comment for
// why that vocabulary is not converted even though some of it is real,
// present data on this box.
func stepEvent(stepType, content string) []agent.ChatEvent {
	switch stepType {
	case stepTypeUser:
		return importer.TextEntry(agent.EntryTypeUser, extractUserRequest(content))
	case stepTypeAssistant:
		return importer.TextEntry(agent.EntryTypeAssistant, strings.TrimSpace(content))
	case stepTypeError:
		return importer.TextEntry(agent.EntryTypeSystem, strings.TrimSpace(content))
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

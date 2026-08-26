package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// SelectionStats reports what selectForDistill removed and what it cost.
// Attached to CompactionResult so a caller can tell a small essence produced
// from a well-filtered transcript from one produced by a distiller that failed
// to say anything — the two are otherwise indistinguishable at the output.
type SelectionStats struct {
	BytesIn   int
	BytesKept int
}

// ResultRepair names a large tool result the agent never commented on. The
// transcript holds no statement of what it meant, so unless one is recovered
// the meaning is lost when the body is reduced.
type ResultRepair struct {
	// Index addresses the entry inside Selection.Entries.
	Index    int
	ToolName string
	// Intent is the rendered call: what was asked for.
	Intent string
	// Body is the full result, ungated by any display cap.
	Body string
}

// Selection is the outcome of deterministic entry selection: what survives,
// what it cost, and which results still need a finding recovered.
//
// A struct rather than a widening tuple -- three of these were already in
// flight and a fourth return value is where call sites start transposing them.
type Selection struct {
	Entries []agent.SessionEntry
	Stats   SelectionStats
	Repairs []ResultRepair
}

// selectForDistill drops transcript entries whose content is synthetic or
// re-derivable, before rendering.
//
// This is SELECTION, not capture, and it belongs here for the same reason
// thinking suppression does (see appendEntryText): the canonical transcript
// stays the durable record cross-engine resume reads, and dropping content
// there would be an unrecoverable loss. Filtering at distill time costs the
// essence nothing that cannot be recovered by looking again.
//
// Errors are always kept. They were measured at 0.1-0.5% of transcript bytes
// across real sessions — filtering them saves nothing and discards the highest
// signal-per-byte content in the file.
func selectForDistill(entries []agent.SessionEntry) Selection {
	var stats SelectionStats
	kept := make([]agent.SessionEntry, 0, len(entries))
	var repairs []ResultRepair

	// A tool RESULT does not carry the arguments its call was made with, and
	// Bash classification needs the command line. Pair result to call by the
	// engine-native call id where the backend supplied one, falling back to the
	// most recent call of the same tool name.
	inputByID := map[string]json.RawMessage{}
	lastInputByName := map[string]json.RawMessage{}

	for i, e := range entries {
		stats.BytesIn += entryBytes(e)

		switch e.Type {
		case agent.EntryTypeToolUse:
			if e.ToolCallID != "" {
				inputByID[e.ToolCallID] = e.ToolInput
			}
			lastInputByName[e.ToolName] = e.ToolInput

		case agent.EntryTypeToolResult:
			if !e.IsError {
				in, ok := inputByID[e.ToolCallID]
				if !ok {
					in = lastInputByName[e.ToolName]
				}
				// Decided HERE rather than in the renderer because it needs
				// sequence context the renderer does not have: whether the
				// agent said anything after this result.
				//
				// The trigger is the ABSENCE OF A STATED FINDING, not the
				// absence of a hook. An engine with no PostToolUse event and
				// one whose agent simply moved on are the same case, and the
				// transcript answers it directly.
				reflected := reflectedAfter(entries, i)
				if !reflected && len(e.ToolOutput) >= agent.DefaultToolReflectBytes {
					repairs = append(repairs, ResultRepair{
						Index:    len(kept),
						ToolName: e.ToolName,
						Intent:   renderToolArgs(in),
						Body:     e.ToolOutput,
					})
				}
				e.ToolOutput = renderResultBody(e.ToolOutput, reflected)
			}
		}

		kept = append(kept, e)
		stats.BytesKept += entryBytes(e)
	}

	return Selection{Entries: kept, Stats: stats, Repairs: repairs}
}

// entryBytes approximates one entry's contribution, for the before/after
// accounting only. It counts the payload the renderer would emit, not the
// headers, so the ratio reflects content rather than framing.
func entryBytes(e agent.SessionEntry) int {
	return len(e.Content) + len(e.ToolInput) + len(e.ToolOutput)
}

// codePayloadArgs name tool-call arguments whose value is generated code that
// now lives in the working tree. They are elided to a size marker rather than
// rendered: the durable fact is that an edit happened and to WHICH file, and
// the bytes themselves are recoverable by reading that file. Everything not
// listed here survives — a shell command, a file path, a subagent brief, a
// question put to the user — because none of those can be looked up anywhere.
//
// This matters more than the byte count suggests. Arguments are rendered
// through a single 500-byte cap applied to the WHOLE argument object, so an
// edit carrying kilobytes of replacement text consumes the entire budget and
// crowds out the file path that identifies it. Eliding the payload spends
// those 500 bytes on the identifying arguments instead.
var codePayloadArgs = map[string]bool{
	"new_string": true,
	"old_string": true,
	"content":    true,
}

// renderToolArgs renders a tool call's arguments for distillation, replacing
// generated-code payloads with a size marker. Unparseable input is returned
// unchanged: a renderer that silently emitted nothing for arguments it could
// not decode would be indistinguishable from a call that had none.
func renderToolArgs(raw json.RawMessage) string {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil {
		return string(raw)
	}
	for k, v := range args {
		if k == questionsArg {
			if q, ok := questionTextOnly(v); ok {
				args[k] = q
			}
			continue
		}
		if !codePayloadArgs[k] {
			continue
		}
		var body string
		if err := json.Unmarshal(v, &body); err != nil {
			body = string(v)
		}
		marker, err := json.Marshal(fmt.Sprintf("<elided %d bytes; read the file>", len(body)))
		if err != nil {
			continue
		}
		args[k] = marker
	}
	out, err := json.Marshal(args)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// questionsArg is the argument carrying a question put to the user, along with
// every answer option's label and description. Only the question asked is
// durable: the option the user CHOSE comes back in the tool result, and the
// options they did not choose describe roads not taken. Reduced to the
// question text so the essence records what was asked without the menu.
const questionsArg = "questions"

// questionTextOnly rewrites a questions array down to each entry's question
// text. Reports false when the value is not the expected shape, leaving the
// caller to pass the original through rather than invent an empty one.
func questionTextOnly(v json.RawMessage) (json.RawMessage, bool) {
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(v, &entries); err != nil {
		return nil, false
	}
	texts := make([]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		q, ok := e["question"]
		if !ok {
			continue
		}
		texts = append(texts, q)
	}
	if len(texts) == 0 {
		return nil, false
	}
	out, err := json.Marshal(texts)
	if err != nil {
		return nil, false
	}
	return out, true
}

// resultShape renders a tool result as its shape rather than its content:
// whether it produced anything, how much, and over how many lines. Callers
// use it for every non-error result -- see appendEntryText for why the body
// is not worth its bytes.
//
// An empty result is reported as empty rather than omitted. A command that
// returned nothing and a command whose output was discarded are different
// facts, and this project's characteristic failure is exactly the one that
// makes them look identical.
func resultShape(out string) string {
	if out == "" {
		return "[no output]"
	}
	lines := strings.Count(out, "\n") + 1
	return fmt.Sprintf("[%d bytes, %d lines]", len(out), lines)
}

// resultExcerptBytes is how much of an UNREFLECTED large result survives.
//
// It is a fallback, and a self-extinguishing one: the excerpt is kept only
// where the agent said nothing about a large result, so the more reliably
// reflection happens, the less raw body distillation carries. That is the
// intended gradient -- a stated finding is strictly better than an excerpt,
// because it says what mattered rather than what came first.
const resultExcerptBytes = 1000

// reflectedAfter reports whether the agent stated anything after the tool
// result at index i, before issuing its next tool call.
//
// Consecutive results are skipped because parallel tool calls return as a run
// and a single following message reflects on all of them. Reaching a new tool
// call, or the end of the transcript, means nothing was said.
func reflectedAfter(entries []agent.SessionEntry, i int) bool {
	for j := i + 1; j < len(entries); j++ {
		switch entries[j].Type {
		case agent.EntryTypeToolResult:
			continue
		case agent.EntryTypeAssistant:
			if strings.TrimSpace(entries[j].Content) != "" {
				return true
			}
		case agent.EntryTypeToolUse:
			return false
		}
	}
	return false
}

// renderResultBody reduces a tool result to what distillation should keep.
//
// A result whose meaning the agent stated is reduced to its SHAPE: the finding
// already carries what mattered, and a truncated fragment of the output is
// neither the information nor a summary of it. A LARGE result that drew no
// comment is different in kind -- nothing else in the transcript records what
// it said -- so a bounded excerpt survives, because the alternative is losing
// it entirely.
//
// The excerpt is the LAST tier of three: a finding stated in-session is best,
// a finding recovered by repairResults is next, and this is what survives when
// both are unavailable. Each tier degrades into the one below it rather than
// into nothing.
func renderResultBody(out string, reflected bool) string {
	shape := resultShape(out)
	if reflected || len(out) < agent.DefaultToolReflectBytes {
		return shape
	}
	excerpt := out
	if len(excerpt) > resultExcerptBytes {
		excerpt = textutil.TruncateBytes(excerpt, resultExcerptBytes)
	}
	return shape + " unreflected; excerpt follows\n" + excerpt
}

// maxErrorBytes bounds an error body. Errors are the one result kind kept for
// their CONTENT rather than their shape -- what broke, and what a fix has to
// answer to -- and truncating one at the ordinary display cap cuts exactly the
// stack trace or compiler output that carries the answer.
//
// Measured across four real transcripts: 26 error results, median 300 bytes,
// largest 1,381, and 9 of them were being truncated by the 500-byte cap.
// Keeping every one whole costs about 750 bytes per session. The bound here is
// an order of magnitude above the largest observed, so it never bites in
// practice and still stops a pathological megabyte error from dominating the
// transcript.
const maxErrorBytes = 16384

// renderErrorBody keeps an error result's content, bounded. Rune-safe: a
// mid-rune split makes the chunk invalid UTF-8, which fails proto3 string
// marshaling and silently turns it into a failure marker.
func renderErrorBody(out string) string {
	if len(out) <= maxErrorBytes {
		return out
	}
	return textutil.TruncateBytes(out, maxErrorBytes)
}

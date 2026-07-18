// Package antigravity implements importer.VendorAdapter for Antigravity
// CLI's (agy) own transcript store:
// ~/.gemini/antigravity-cli/brain/<uuid>/.system_generated/logs/
// transcript_full.jsonl. It copies the codex reference adapter's shape
// (internal/transcript/importer/codex) — a small envelope/payload type set, a
// Convert that streams the file in order, and a colocated fixture test
// runnable in total isolation from the rest of the module — but its OUTPUT
// surface is deliberately smaller: antigravity has no StructuredChat
// capability at all (no internal/antigravity/chat.go), so ctxloom's own
// canonical mapping table for it (docs/transcript-schema.md §2c) names only
// two step types as carrying anything canonical — USER_INPUT -> entry.type
// "user", PLANNER_RESPONSE -> entry.type "assistant" — and lists model
// reasoning and tool invocation/output as "(none)" for antigravity's native
// format. A real transcript_full.jsonl captured on this box (see
// testdata/MANIFEST.json) in fact carries a great deal more than that table
// documents: a "thinking" field on some PLANNER_RESPONSE steps, a
// "tool_calls" array naming the action about to run, and step types this
// package never converts (CONVERSATION_HISTORY, CHECKPOINT, GENERIC,
// LIST_DIRECTORY, RUN_COMMAND, CODE_ACTION, VIEW_FILE, SYSTEM_MESSAGE,
// ERROR_MESSAGE). This adapter follows the documented mapping table exactly
// and does NOT surface any of that extra vocabulary, for two reasons: (1) the
// "tool completed" steps (LIST_DIRECTORY/RUN_COMMAND/CODE_ACTION/VIEW_FILE)
// carry no call-id correlating them back to the tool_calls entry that started
// them, and their content is unstructured human-readable narrative
// ("Created At: ...\nCompleted At: ...\n<prose>"), not a structured result —
// inventing a tool_use/tool_result pair from that would fabricate a
// call-id/output shape the format does not actually have, exactly what
// importer.VendorAdapter's doc comment and this project's "don't invent
// structure" discipline forbid; (2) surfacing "thinking" alone, without the
// tool-call narrative it is almost always interleaved with, would be a
// partial, misleadingly tidy fidelity increase the schema doc does not
// describe. This is a judgment call flagged for review, not a hard technical
// wall: docs/transcript-schema.md §2c could be revised to add a `thinking`
// row for antigravity in a follow-up if that fidelity is wanted.
package antigravity

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/importer"
)

// Adapter implements importer.VendorAdapter for antigravity's
// transcript_full.jsonl store. Stateless: every field Convert needs lives in
// the per-call converter it constructs, so one Adapter value is safe to
// reuse (or share as a package var) across concurrent Convert calls for
// different files.
type Adapter struct{}

var _ importer.VendorAdapter = Adapter{}

// New returns an antigravity Adapter. A plain Adapter{} literal works
// identically — this exists only so callers outside the package don't need
// to know the type is a zero-size struct they can construct directly.
func New() Adapter { return Adapter{} }

// Convert reads the antigravity transcript_full.jsonl file at src and
// appends its conversation to rec in the file's own order. See
// importer.VendorAdapter's doc comment for the general contract (malformed
// lines skipped, not fatal; a rec.Record failure or ctx cancellation IS
// fatal). Locating WHICH brain/<uuid>/ directory holds the right file for a
// given ctxloom session is a caller concern (this is the "locate" problem
// the old, deleted internal/antigravity reader got wrong by keying a global
// store the workDir couldn't resolve — docs/transcript-schema.md §2b/§8) —
// Convert only ever parses a src path it is handed.
//
// The whole file is read into memory in one os.ReadFile call rather than
// streamed through a bufio.Reader: a transcript_full.jsonl file is one
// session's own step log (not an ever-growing rollout that could outlive
// available memory the way a long-lived codex/kiro session store might), and
// reading it whole sidesteps a bufio.Scanner's default per-token size cap
// entirely — a PLANNER_RESPONSE step's "thinking" field alone can run to
// several kilobytes on a real capture (see testdata/MANIFEST.json), and a
// capped Scanner would hard-fail the entire file on the first such line,
// violating the degrade-to-partial contract this package must not break.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("antigravity: open %s: %w", src, err)
	}
	return convertLines(ctx, rec, splitNonEmptyLines(data))
}

// splitNonEmptyLines breaks data on '\n' and drops any line that is empty
// after trimming surrounding whitespace (a trailing newline, a blank line
// between steps).
func splitNonEmptyLines(data []byte) [][]byte {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// Package antigravity implements vendorreader.VendorAdapter for Antigravity
// CLI's (agy) own transcript store:
// ~/.gemini/antigravity-cli/brain/<uuid>/.system_generated/logs/
// transcript_full.jsonl. It copies the codex reference adapter's shape
// (internal/transcript/vendorreader/codex) — a small envelope/payload type set, a
// Convert that streams the file in order, and a colocated fixture test
// runnable in total isolation from the rest of the module — but its OUTPUT
// surface is deliberately smaller. antigravity DOES offer StructuredChat
// (internal/antigravity/chat.go), but implements it as a bespoke PROSE driver
// over `agy -p` instead of through internal/acp.NewChatDriver like every
// other engine: prose in, prose out, with no reasoning, tool-call or
// turn-accounting events anywhere in it. So ctxloom's own canonical mapping
// table for antigravity (docs/transcript-schema.md §2c) names only three step
// types as carrying anything canonical — USER_INPUT -> entry.type "user",
// PLANNER_RESPONSE -> entry.type "assistant", ERROR_MESSAGE -> entry.type
// "system" — and lists model reasoning and tool invocation/output as "(none)"
// for antigravity's native format. A real transcript_full.jsonl captured on this box (see
// testdata/MANIFEST.json) in fact carries a great deal more than that table
// documents: a "thinking" field on some PLANNER_RESPONSE steps, a
// "tool_calls" array naming the action about to run, and step types this
// package never converts (CONVERSATION_HISTORY, CHECKPOINT, GENERIC,
// LIST_DIRECTORY, RUN_COMMAND, CODE_ACTION, VIEW_FILE, SYSTEM_MESSAGE).
// This adapter follows the documented mapping table exactly
// and does NOT surface any of that extra vocabulary, for two reasons: (1) the
// "tool completed" steps (LIST_DIRECTORY/RUN_COMMAND/CODE_ACTION/VIEW_FILE)
// carry no call-id correlating them back to the tool_calls entry that started
// them, and their content is unstructured human-readable narrative
// ("Created At: ...\nCompleted At: ...\n<prose>"), not a structured result —
// inventing a tool_use/tool_result pair from that would fabricate a
// call-id/output shape the format does not actually have, exactly what
// vendorreader.VendorAdapter's doc comment and this project's "don't invent
// structure" discipline forbid; (2) surfacing "thinking" alone, without the
// tool-call narrative it is almost always interleaved with, would be a
// partial, misleadingly tidy fidelity increase the schema doc does not
// describe. This is a judgment call flagged for review, not a hard technical
// wall: docs/transcript-schema.md §2c could be revised to add a `thinking`
// row for antigravity in a follow-up if that fidelity is wanted.
package antigravity

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Adapter implements vendorreader.VendorAdapter for antigravity's
// transcript_full.jsonl store. Stateless: every field Convert needs lives in
// the per-call converter it constructs, so one Adapter value is safe to
// reuse (or share as a package var) across concurrent Convert calls for
// different files.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// VersionedAdapters declares which antigravity CLI versions this adapter is
// validated to read. Selection is (engine, RECORDED version) -> adapter
// (vendorreader.SelectAdapter); a version outside every range here REFUSES,
// and there is deliberately no default to fall through to.
//
// The 1.x line. agy is not installed on any host this project can reach, so
// this range rests entirely on the lock's 1.1.4 pin and the CI drift gate over
// it — narrower than it looks in practice, since any 1.x version outside what
// the pipeline has validated is a range this project should tighten rather
// than trust.
//
// ValidatedVersion cites .github/engine-versions.env, the tested-version lock
// CI keeps honest — it is what makes the range above evidence rather than
// hope, and TestVendorReaderRanges_ContainThePinnedTestedVersion holds the two
// together.
//
// A declared var, in the shape of claude.ClaudeACPTransport and the other
// per-engine declarations this repo keeps beside their engine: it is a FACT
// this package states about itself, read once into
// operations.vendorReaderRegistry, not a computation.
var VersionedAdapters = []vendorreader.VersionedAdapter{{
	Adapter:          Adapter{},
	Versions:         vendorreader.VersionRange{MinInclusive: "1.0.0", MaxExclusive: "2.0.0"},
	ValidatedVersion: "1.1.4",
}}

// Convert reads the antigravity transcript_full.jsonl file at src and
// appends its conversation to rec in the file's own order. See
// vendorreader.VendorAdapter's doc comment for the general contract (malformed
// lines skipped, not fatal; a rec.Record failure or ctx cancellation IS
// fatal). Locating WHICH brain/<uuid>/ directory holds the right file for a
// given ctxloom session is a caller concern (this is the "locate" problem
// the old, deleted internal/antigravity reader got wrong by keying a global
// store the workDir couldn't resolve — docs/transcript-schema.md §2b/§8) —
// Convert only ever parses a src path it is handed.
//
// Lines are read via the same vendorreader.OpenAndReadJSONLLines every JSONL-per-
// session engine's adapter uses (codex, claude): an unbounded bufio.Reader,
// not a capped bufio.Scanner — a PLANNER_RESPONSE step's "thinking" field
// alone can run to several kilobytes on a real capture (see
// testdata/MANIFEST.json), and a capped Scanner would hard-fail the entire
// file on the first such line, violating the degrade-to-partial contract
// this package must not break.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	lines, err := vendorreader.OpenAndReadJSONLLines("antigravity", src)
	if err != nil {
		return err
	}
	return convertLines(ctx, rec, lines)
}

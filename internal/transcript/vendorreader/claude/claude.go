// Package claude implements vendorreader.VendorAdapter for claude-code's own
// interactive-TUI transcript store: ~/.claude/projects/<slug>/<uuid>.jsonl.
// It copies the shape of internal/transcript/vendorreader/codex, the reference
// adapter (docs/transcript-schema.md §8, tough-cloud-writer-a): a small
// envelope/payload type set, a two-pass Convert (session metadata first, then
// a streamed entry pass), and a colocated fixture test runnable in total
// isolation from the rest of the module.
//
// Unlike codex's rollout-*.jsonl, claude's file carries NO outer envelope —
// each line IS the record, discriminated by its own top-level "type" (user |
// assistant | progress | queue-operation | system | ...; docs/
// transcript-schema.md §2b). This adapter only extracts "user" and
// "assistant" lines; every other type is administrative UI/session state
// (hook progress notices, queue bookkeeping, slash-command/title/mode
// bookkeeping, turn-duration telemetry) with no conversational content of
// its own, skipped exactly like an unrecognized response_item variant is
// skipped in codex.
//
// The one bug this package exists to fix is NOT in this file: it lives one
// layer up, in the (not-yet-rebuilt) locate step that turns a cwd into this
// file's path (the deleted scraper's cwd→directory-slug re-encoding landing on
// the wrong filename, docs/transcript-schema.md §2b "tall-grab"). Convert
// here takes the transcript path directly, exactly like codex's Convert — it
// has no cwd to get wrong.
package claude

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Adapter implements vendorreader.VendorAdapter for claude's
// ~/.claude/projects/<slug>/<uuid>.jsonl store. Stateless: every field
// Convert needs lives in the per-call converter it constructs, so one
// Adapter value is safe to reuse (or share as a package var) across
// concurrent Convert calls for different files.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// Convert reads the claude transcript JSONL file at src and appends its
// conversation to rec in the file's own order. See vendorreader.VendorAdapter's
// doc comment for the general contract (malformed lines skipped, not fatal;
// a rec.Record failure or ctx cancellation IS fatal). Deliberately mirrors
// codex.Adapter.Convert's shape (open, readJSONLLines, convertLines): the
// two-pass shape is the reference pattern every vendorreader.VendorAdapter
// copies from codex (codex.go's package doc); the line-reading step itself
// is not a copy at all — both call the same vendorreader.OpenAndReadJSONLLines.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	lines, err := vendorreader.OpenAndReadJSONLLines("claude", src)
	if err != nil {
		return err
	}
	return convertLines(ctx, rec, lines)
}

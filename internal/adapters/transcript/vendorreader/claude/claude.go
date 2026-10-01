// Package claude implements vendorreader.VendorAdapter for claude-code's own
// interactive-TUI transcript store: ~/.claude/projects/<slug>/<uuid>.jsonl.
// It follows the shape every vendorreader adapter shares (ADR 0035 for why
// one exists at all): a small envelope/payload type set, a two-pass Convert
// (session metadata first, then a streamed entry pass), and a colocated
// fixture test runnable in total isolation from the rest of the module.
//
// Claude's file carries NO outer envelope — each line IS the record,
// discriminated by its own top-level "type" (user |
// assistant | progress | queue-operation | system | ...; ADR 0035, "The
// native per-engine files the old readers scraped"). This adapter only
// extracts "user" and "assistant" lines; every other type is administrative
// UI/session state (hook progress notices, queue bookkeeping,
// slash-command/title/mode bookkeeping, turn-duration telemetry) with no
// conversational content of its own, and is skipped.
//
// The one bug this package exists to fix is NOT in this file: it lives one
// layer up, in the locate step that resolves this file's path
// (operations.locateBoundTranscript). The deleted scraper re-encoded a cwd
// into claude's directory slug and landed on the wrong filename (ADR 0035
// names it); the rebuilt step re-encodes nothing — it stats the path claude's
// own SessionStart hook bound forward. Convert here takes the transcript path
// directly — it has no cwd to get wrong.
package claude

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
)

// Adapter implements vendorreader.VendorAdapter for claude's
// ~/.claude/projects/<slug>/<uuid>.jsonl store. Stateless: every field
// Convert needs lives in the per-call converter it constructs, so one
// Adapter value is safe to reuse (or share as a package var) across
// concurrent Convert calls for different files.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// VersionedAdapters declares which claude-code CLI versions this adapter is
// validated to read. Selection is (engine, RECORDED version) -> adapter
// (vendorreader.SelectAdapter); a version outside every range here REFUSES,
// and there is deliberately no default to fall through to.
//
// The 2.x line is one format as far as this adapter is concerned: a
// project-scoped JSONL file per session, one JSON object per line. The range
// covers the lock's pin and whatever 2.x a user happens to run, and covering the whole major line is the claim that claude-code has not
// reshaped its per-line schema within 2.x. It stops at 3.0.0 because a major
// bump is exactly where a vendor is entitled to reshape it, and an unbounded
// range would assert compatibility with a format nobody has seen.
//
// ValidatedVersion cites .github/engine-versions.env, the tested-version lock
// CI keeps honest — it is what makes the range above evidence rather than
// hope, and TestVendorReaderRanges_ContainThePinnedTestedVersion holds the two
// together.
//
// A declared var, in the shape of claude.ClaudeACPTransport and the other
// per-engine declarations this repo keeps beside their engine: it is a FACT
// this package states about itself, declared once on the engine's descriptor
// (hosting.Hosting.TranscriptReaders), not a computation.
var VersionedAdapters = []vendorreader.VersionedAdapter{{
	Adapter:          Adapter{},
	Range:            vendorreader.VersionRange{MinInclusive: "2.0.0", MaxExclusive: "3.0.0"},
	ValidatedVersion: "2.1.286",
}}

// Convert reads the claude transcript JSONL file at src and appends its
// conversation to rec in the file's own order. See vendorreader.VendorAdapter's
// doc comment for the general contract (malformed lines skipped, not fatal;
// a rec.Record failure or ctx cancellation IS fatal). The shape (open, read
// lines, convertLines) is the two-pass pattern every
// vendorreader.VendorAdapter follows (vendorreader.ConvertJSONLLines); the
// line-reading step is the shared vendorreader.OpenAndReadJSONLLines, not a
// per-adapter copy.
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	lines, err := vendorreader.OpenAndReadJSONLLines("claude", src)
	if err != nil {
		return err
	}
	_, err = convertLines(ctx, rec, lines)
	return err
}

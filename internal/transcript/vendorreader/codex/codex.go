// Package codex implements vendorreader.VendorAdapter for codex CLI's own
// transcript store: ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl. It is the
// REFERENCE adapter (docs/transcript-schema.md §8, tough-cloud-writer-a) —
// every other engine's reader (kiro/claude) copies this
// package's shape: a small envelope/payload type set, a two-pass Convert
// (session metadata first, then a streamed entry pass), and a colocated
// fixture test runnable in total isolation from the rest of the module.
//
// The one bug this package exists to fix (docs/transcript-schema.md §2b/§8,
// "the confirmed break (lived-zone)"): codex's rollout file is an ENVELOPE,
// {timestamp, type, payload}, with every real field nested under payload —
// the deleted scraper (internal/codex/capabilities.go, removed in tough-cloud
// S5) declared a FLAT struct with type/role/content/tool_name at the top
// level, so every real field silently decoded to its zero value and the
// reader returned a zero-entry session for every real rollout file it ever
// read. See rollout.go's rolloutLine doc comment for the fix.
package codex

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// Adapter implements vendorreader.VendorAdapter for codex's rollout-*.jsonl store.
// Stateless: every field Convert needs lives in the per-call converter it
// constructs, so one Adapter value is safe to reuse (or share as a package
// var) across concurrent Convert calls for different files.
type Adapter struct{}

var _ vendorreader.VendorAdapter = Adapter{}

// VersionedAdapters declares which codex CLI versions this adapter is
// validated to read. Selection is (engine, RECORDED version) -> adapter
// (vendorreader.SelectAdapter); a version outside every range here REFUSES,
// and there is deliberately no default to fall through to.
//
// Narrow ON PURPOSE, unlike the other three. codex is pre-1.0, so its minor
// version is where breaking changes are allowed to land, and its
// envelope-vs-flat rollout parse is already named in
// internal/lm/grpc/canonical_source.go as one of the four defects the DELETED
// per-engine scrapers were removed for — a format this project has already
// been burned by reading too confidently. The lock pins 0.144.6 and this host
// runs 0.144.4; both are inside 0.144.x, and anything outside it refuses
// until someone validates it.
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
	Versions:         vendorreader.VersionRange{MinInclusive: "0.144.0", MaxExclusive: "0.145.0"},
	ValidatedVersion: "0.144.6",
}}

// Convert reads the codex rollout-*.jsonl file at src and appends its
// conversation to rec in the file's own order. See vendorreader.VendorAdapter's
// doc comment for the general contract (malformed lines skipped, not fatal;
// a rec.Record failure or ctx cancellation IS fatal).
func (Adapter) Convert(ctx context.Context, rec transcript.Recorder, src string) error {
	// Cancellation is checked before anything expensive happens. Reading a
	// multi-megabyte rollout fully into memory and then discovering the caller
	// gave up is the whole cost of the import, paid for nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	lines, err := vendorreader.OpenAndReadJSONLLines("codex", src)
	if err != nil {
		return err
	}
	return convertLines(ctx, rec, lines)
}

// Package vendorreader defines the shared contract every per-engine vendor
// reader implements: parse ONE vendor-native transcript (codex's rollout
// JSONL, kiro's session store, claude's project JSONL) into ctxloom's own
// canonical agent.ChatEvent stream and append it
// through the SAME transcript.Recorder sink the live structured-chat tee
// (internal/transcript/recorder.go's Tee/TeeAndClose) already writes
// through. This is the point of the whole package: a session read from its
// vendor's store and a tee'd session must land in byte-for-byte the same
// on-disk schema (transcript.acp.jsonl), never a second format a downstream
// consumer would need to special-case. See docs/transcript-schema.md §8 for
// why a vendor reader exists at all (reversing S5's scraper deletion for
// interactive-pty and pre-capture sessions, done through the canonical
// Recorder this time instead of a bespoke SessionHistory scraper) and
// internal/transcript/vendorreader/codex for the reference implementation every
// other engine's reader copies.
//
// READER, NOT IMPORTER — and NOT the deleted scrapers either. These are not
// one-time imports into a ctxloom-owned archive: a vendor reader is consulted
// whenever a conversation is needed and transforms the vendor's own store
// into ctxloom's canonical form ON READ (task virtuous-evil). Do not
// confuse this package with the per-engine
// Backend.History() SCRAPERS deleted in S5 as PROVEN BROKEN (their defects
// are named in internal/lm/grpc/canonical_source.go: codex's
// envelope-vs-flat parse, claude's wrong-filename, kiro's v1-vs-v2-sqlite,
// antigravity's global-store mis-key). Both were implementations of the same
// interface; the scrapers were deleted for being WRONG, not for being the
// wrong shape. Comments elsewhere that say "the deleted reader" or "the
// deleted scraper" mean those scrapers, not this package.
//
// This package is deliberately NOT named "vendor": a directory literally
// named vendor anywhere in a Go module is special-cased by the toolchain
// (reserved for the vendoring mechanism) and silently excluded from `...`
// wildcard package matching — go build/test/vet ./... would never see a
// package living at .../vendor/codex at all, even though an EXPLICIT path
// like ./internal/transcript/vendor/codex/... resolves fine (confirmed by
// hitting exactly this while developing the codex adapter: it ran individually
// but never appeared in a full `go test ./...`). "vendorreader" avoids the
// landmine while keeping the same meaning.
package vendorreader

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

// VendorAdapter converts one vendor-native transcript into ctxloom's
// canonical schema by appending to rec.
//
// src is an engine-specific locator for the transcript to convert. For a
// JSONL-per-session engine (codex today; claude is the same
// shape) this is simply the transcript file's path. A database-backed store
// (kiro's v2 sqlite conversations_v2, per docs/transcript-schema.md §2b)
// cannot be located by a bare path alone — that engine's adapter will need a
// composite locator (e.g. "<db-path>#<conversation-id>") once it's built;
// this interface does not change shape to accommodate that, the string just
// carries a richer convention for that one implementation.
//
// Convert does not construct rec: the caller (an interactive-pty on-exit
// hook, a backfill CLI command, a release-monitoring job validating one
// engine's parser against a fresh transcript) owns Recorder construction
// (transcript.NewRecorder) and lifecycle (Close) — the harp/engine pairing
// and RawPolicy are call-site decisions, not something a parser should
// decide on its own. Convert must tolerate being handed a Recorder that
// already has lines written to it (e.g. a resumed/retried read): every
// Recorder implementation already tolerates repeated Record calls across its
// lifetime (see recorder.go's Record doc comment), and Convert adds no
// requirement beyond "call Record in src's own order."
//
// A malformed line/row in src is skipped, never fatal for the whole
// transcript — the same degrade-to-partial contract
// agent.SessionStore.ParseSessionFile and transcript.ParseTranscriptFile
// already guarantee on the READ side (docs/transcript-schema.md §6.4). A
// reader that aborted on the first bad byte would turn one corrupt line
// into an entirely lost session, exactly the failure mode this package
// exists to avoid. Convert returns an error only for a structural failure
// that stops all further progress: src cannot be opened/read, ctx is
// cancelled, or rec.Record itself fails (a live disk-write failure is not
// safe to paper over by skipping and continuing — unlike a single
// unparseable vendor line, it means the sink itself is no longer trustworthy).
type VendorAdapter interface {
	Convert(ctx context.Context, rec transcript.Recorder, src string) error
}

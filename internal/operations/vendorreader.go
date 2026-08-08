// This file wires the four per-engine vendorreader.VendorAdapter implementations
// (internal/transcript/vendorreader/{codex,claude,antigravity,kiro}) into the two
// call sites that actually need a converted transcript: the interactive-pty
// exit seam (internal/cli/run.go, right where transcript.RecordOneshot hooks
// the oneshot exit) and the `session backfill` command (session_cmd.go),
// which runs the identical conversion over already-indexed old sessions.
// Closes docs/transcript-schema.md §8's "interactive-pty gap": an engine
// driven through its own interactive TUI has no ctxloom memory today because
// the structured tee (Tee/TeeAndClose) can never reach a pty — this is the
// missing other half, reading the engine's OWN transcript back after the
// fact instead.
package operations

import (
	"context"
	"fmt"
	"os"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
	antigravityreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/antigravity"
	claudereader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/claude"
	codexreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/codex"
	kiroreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/kiro"
)

// vendorLocate resolves the vendor-native transcript locator (the src string
// vendorreader.VendorAdapter.Convert expects — a bare file path for every engine
// but kiro, kiro's own "<db-path>#<conversation-id>" composite for it) for
// one indexed session entry. ok=false means "nothing to convert" — an
// unbound session, a bind whose file has since vanished, an engine this
// registry doesn't cover — which is the ordinary case for most entries, not
// a failure a caller should ever warn about.
type vendorLocate func(ctx context.Context, e sessions.Entry) (src string, ok bool)

// vendorReaderEntry pairs one engine's version-scoped adapters with its
// locate func. adapters is a LIST because a vendor's transcript format moves
// between releases: ctxloom may carry several whole-file adapters per engine,
// each declaring the version range it was validated against, and the session's
// RECORDED engine version picks among them (vendorreader.SelectAdapter). A
// version matching none of them refuses — see version.go for why there is no
// default to fall through to.
type vendorReaderEntry struct {
	adapters []vendorreader.VersionedAdapter
	locate   vendorLocate
}

// vendorReaderRegistry maps a backend registry name — the SAME name
// backends.descriptors registers it under (agent.NewBaseBackend's first arg:
// config.BackendClaudeCode "claude-code", "codex", "kiro", "antigravity"),
// the plugin's own Info RPC reports, and transcript.RecordOneshot's engine
// param already carries — to its VendorAdapter + locate pair. This is
// deliberately the REGISTRY name, not the reader packages' own short test
// names ("claude"/"codex"/"kiro"/"antigravity" in their _test.go fixtures):
// using anything else would make a harp's oneshot-mode entries (Engine:
// "claude-code") and its interactive-mode entries (this file) disagree about
// which engine wrote a canonical transcript's Engine field.
//
// opencode/acp/mock have no entry: opencode's own native reader
// (internal/opencode/capabilities.go) was never broken and stays wired
// separately (docs/transcript-schema.md §8's explicit carve-out); acp/mock
// have no vendor-native transcript store of their own to import from.
//
// Three of the four engines PREFER the already-bound transcript path
// (locateBoundTranscript): the SessionStart bind hook (claude/codex) or the
// PreToolUse fallback (antigravity) already resolved the vendor file for
// ctxloom's OWN index — see sessions.Manager.BindSession — so there is no
// path-derivation logic to duplicate here, and no chance of resurrecting the
// deleted reader's claude cwd→slug bug (docs/transcript-schema.md §8). kiro
// is the one exception, wired separately in vendorreader_kiro.go: its bind
// (on the rare path where one lands at all) is a session_id, not a file
// path, because a single sqlite db holds every conversation.
var vendorReaderRegistry = map[string]vendorReaderEntry{
	config.BackendClaudeCode: {adapters: claudereader.VersionedAdapters, locate: locateBoundTranscript},
	"codex":                  {adapters: codexreader.VersionedAdapters, locate: locateBoundTranscript},
	"antigravity":            {adapters: antigravityreader.VersionedAdapters, locate: locateBoundTranscript},
	"kiro":                   {adapters: kiroreader.VersionedAdapters, locate: locateKiroConversation},
}

// VendorReaderEngineNames returns the backend names vendorReaderRegistry
// covers (one of the four independently-maintained engine-identity
// rosters found spread across the codebase — see
// tests/arch/engine_identity_arch_test.go's TestArch_EngineIdentityRosters_
// MembersAreRegisteredBackends, which validates every name returned here is
// still a real, currently-registered internal/lm/backends name). Exported
// read-only so that gate can reach this package's otherwise-unexported
// roster without operations importing backends any more than it already
// does, and without the gate importing operations' internals.
func VendorReaderEngineNames() []string {
	names := make([]string, 0, len(vendorReaderRegistry))
	for name := range vendorReaderRegistry {
		names = append(names, name)
	}
	return names
}

// locateBoundTranscript is the locate func shared by every JSONL-per-session
// engine (claude/codex/antigravity): sessions.Entry.TranscriptPath already
// carries the vendor file's path (bound forward by the SessionStart hook or
// its PreToolUse-fallback equivalent), so this only stats it — a stale or
// since-removed bind degrades to "not found" rather than handing Convert a
// dead path to fail on.
func locateBoundTranscript(_ context.Context, e sessions.Entry) (string, bool) {
	if e.TranscriptPath == "" {
		return "", false
	}
	if _, err := os.Stat(e.TranscriptPath); err != nil {
		return "", false
	}
	return e.TranscriptPath, true
}

// ConvertVendorTranscript imports harp e's vendor-native transcript into
// ctxloom's canonical transcript.jsonl through a fresh
// transcript.Recorder, and is the ONE function both the interactive-pty exit
// seam and `session backfill` call — the same conversion, triggered from two
// different moments (just-exited vs. already-indexed).
//
// converted reports whether an import was actually ATTEMPTED (Convert
// invoked), not whether it produced any canonical lines — Convert's own
// degrade-to-partial contract (vendorreader.VendorAdapter's doc comment) means a
// vendor file that parses to zero real entries is a legitimate outcome, not
// a signal this function should try to distinguish from "nothing to
// import." converted=false, err=nil covers every "nothing to do" case: e's
// backend isn't registered, e has no locatable vendor transcript, or a
// canonical transcript already exists for the harp.
//
// converted=false with an ERROR is the REFUSAL case, and it is new: a session
// whose recorded engine version matches no adapter ctxloom carries — including
// a session that records no version at all, which every session predating
// version recording does — is not read. Nothing is attempted, nothing is
// written, and the error says which version was refused against which
// validated ranges. See vendorreader/version.go for why this never falls
// through to a "newest" adapter.
//
// Idempotent BY NON-REPETITION, not by content-diffing: Convert re-reads the
// vendor source from its own beginning every call (vendorreader.VendorAdapter
// has no incremental/resume concept — see adapter.go's doc comment on a
// Recorder that "already has lines written to it"), so calling this twice
// for the same harp after the first call actually wrote a canonical file
// would DUPLICATE every entry, not merge them. The guard against that is
// hasCanonicalTranscript below: once ANY canonical transcript exists for a
// harp, this is a permanent no-op for it, by design — never "check if the
// vendor file grew and reconvert."
//
// Best-effort at the CALLER's discretion: this function returns a real error
// when Convert or Recorder construction fails (so a caller can tell "genuinely
// nothing to import" from "tried and failed" — the two need different UX,
// silence vs. a warning/report row) — it does not swallow errors itself.
//
// A Convert failure that happened AFTER some lines were already recorded
// has its partial canonical file removed before returning: without
// that, hasCanonicalTranscript's presence-only guard would treat the partial
// file as a complete one forever, silently masking the original failure on
// every later call for this harp instead of allowing a genuine retry.
func ConvertVendorTranscript(ctx context.Context, e sessions.Entry) (converted bool, err error) {
	reg, ok := vendorReaderRegistry[e.Backend]
	if !ok || e.HarpName == "" {
		return false, nil
	}
	if hasCanonicalTranscript(e.HarpName) {
		return false, nil
	}
	src, ok := reg.locate(ctx, e)
	if !ok {
		return false, nil
	}

	// SELECT AFTER LOCATE, deliberately. A refusal is a real, actionable
	// signal — "you have a transcript ctxloom will not parse, and here is
	// why" — and it should only be raised about a session that actually HAS
	// one. Selecting first would make every unbound or already-vanished
	// session shout about an unknown version instead of being the quiet
	// nothing-to-do it is, and a refusal that fires constantly stops being
	// read.
	//
	// Everything past this point is the OTHER failure level: with a validated
	// adapter chosen, a malformed line degrades to partial rather than
	// refusing (vendorreader.VendorAdapter's contract). The two must not be
	// collapsed — see vendorreader/version.go's header.
	adapter, aerr := vendorreader.SelectAdapter(e.Backend, e.EngineVersion, e.HarpName, reg.adapters)
	if aerr != nil {
		return false, aerr
	}

	rec, err := transcript.NewRecorder(e.HarpName, e.Backend)
	if err != nil {
		return true, fmt.Errorf("open recorder for %s: %w", e.HarpName, err)
	}

	cerr := adapter.Convert(ctx, rec, src)
	_ = rec.Close()
	if cerr != nil {
		// Convert can fail AFTER already recording some lines (a
		// bad byte partway through a large transcript), and
		// transcript.Recorder creates its file on the first SUCCESSFUL
		// Record (recorder.go's NewRecorder doc) — so a failure here can
		// still leave a real, non-empty canonical file on disk.
		// hasCanonicalTranscript's guard is presence-only (by design: see
		// its own doc comment on why content-diffing is wrong), so without
		// cleanup THIS partial file would be indistinguishable from a
		// complete one on every future call for this harp — permanently
		// masking the failure instead of allowing a retry once the vendor
		// format or a parser bug is fixed. Best-effort: removal failing is
		// not itself reported, since the conversion error is already the
		// actionable fact.
		if p, perr := paths.HarpCanonicalTranscriptPath(e.HarpName); perr == nil {
			_ = os.Remove(p)
		}
		return true, fmt.Errorf("convert %s transcript for %s: %w", e.Backend, e.HarpName, cerr)
	}
	// Convert succeeding is NOT the same fact as bytes landing on
	// disk. transcript.Recorder only creates its canonical file on the FIRST
	// SUCCESSFUL Record, so a Convert that (legitimately, per
	// vendorreader.VendorAdapter's own degrade-to-partial contract) wrote zero
	// entries leaves no canonical file at all. Reporting converted=true here
	// regardless used to make `session backfill` print "converted: <harp>"
	// for a harp with nothing delivered — and because no file exists,
	// hasCanonicalTranscript's guard above never catches it, so EVERY later
	// backfill run repeated the same false report, not just once. Fold this
	// into the ordinary "nothing to do" (Skipped) outcome instead: a caller
	// that wants to know Convert was genuinely attempted-but-empty can still
	// distinguish it from "not attempted at all" by checking
	// hasCanonicalTranscript(harp) itself.
	if !hasCanonicalTranscript(e.HarpName) {
		return false, nil
	}
	return true, nil
}

// hasCanonicalTranscript reports whether harp already has a canonical
// transcript.jsonl on disk — ConvertVendorTranscript's idempotency guard
// (see its doc comment for why presence, not a staleness/mtime comparison,
// is the right check here). Resolved via
// paths.ResolveHarpCanonicalTranscriptPath, NOT HarpCanonicalTranscriptPath
// directly: a pre-rename session has only the legacy transcript.acp.jsonl on
// disk, and this guard must see that as "already captured" too — otherwise
// a harp that already has a legacy-named canonical transcript would get
// re-converted, duplicating every entry into a second, current-named file
// (see ConvertVendorTranscript's doc comment on why this is NOT idempotent
// by content-diffing).
func hasCanonicalTranscript(harp string) bool {
	p, err := paths.ResolveHarpCanonicalTranscriptPath(harp)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

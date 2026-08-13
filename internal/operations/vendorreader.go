// This file wires the three per-engine vendorreader.VendorAdapter implementations
// (internal/transcript/vendorreader/{codex,claude,kiro}) into the two
// call sites that actually need a converted transcript: the interactive-pty
// exit seam (internal/cli/run.go, right where transcript.RecordOneshot hooks
// the oneshot exit) and the recover_session MCP tool (mcp_tools_memory.go),
// which runs the identical conversion over already-indexed old sessions.
// Closes docs/transcript-schema.md §8's "interactive-pty gap": an engine
// driven through its own interactive TUI has no ctxloom memory today because
// the structured tee (Tee/TeeAndClose) can never reach a pty — this is the
// missing other half, reading the engine's OWN transcript back after the
// fact instead.
package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/transcript"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
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
// config.BackendClaudeCode "claude-code", "codex", "kiro"),
// the plugin's own Info RPC reports, and transcript.RecordOneshot's engine
// param already carries — to its VendorAdapter + locate pair. This is
// deliberately the REGISTRY name, not the reader packages' own short test
// names ("claude"/"codex"/"kiro" in their _test.go fixtures):
// using anything else would make a harp's oneshot-mode entries (Engine:
// "claude-code") and its interactive-mode entries (this file) disagree about
// which engine wrote a canonical transcript's Engine field.
//
// opencode/acp/mock have no entry: opencode's own native reader
// (internal/opencode/capabilities.go) was never broken and stays wired
// separately (docs/transcript-schema.md §8's explicit carve-out); acp/mock
// have no vendor-native transcript store of their own to import from.
//
// Two of the three engines PREFER the already-bound transcript path
// (locateBoundTranscript): the SessionStart bind hook (claude/codex) already
// resolved the vendor file for ctxloom's OWN index — see
// sessions.Manager.BindSession — so there is no path-derivation logic to
// duplicate here, and no chance of resurrecting the deleted reader's claude
// cwd→slug bug (docs/transcript-schema.md §8). kiro is the one exception,
// wired separately in vendorreader_kiro.go: its bind (on the rare path where
// one lands at all) is a session_id, not a file path, because a single
// sqlite db holds every conversation.
var vendorReaderRegistry = map[string]vendorReaderEntry{
	config.BackendClaudeCode: {adapters: claudereader.VersionedAdapters, locate: locateBoundTranscript},
	"codex":                  {adapters: codexreader.VersionedAdapters, locate: locateBoundTranscript},
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
// engine (claude/codex): sessions.Entry.TranscriptPath already carries the
// vendor file's path (bound forward by the SessionStart hook), so this only
// stats it — a stale or since-removed bind degrades to "not found" rather
// than handing Convert a dead path to fail on.
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
// seam and the recover_session tool call — the same conversion, triggered from two
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
// harp, this is a permanent no-op for it, by design.
//
// A session that is STILL GROWING is the one case that guard answers wrongly,
// and RefreshVendorTranscript — not this function — is the answer to it.
//
// Best-effort at the CALLER's discretion: this function returns a real error
// when Convert or Recorder construction fails (so a caller can tell "genuinely
// nothing to import" from "tried and failed" — the two need different UX,
// silence vs. a warning/report row) — it does not swallow errors itself.
//
// A Convert failure never leaves a partial canonical file behind: conversion
// writes to a temporary sibling that is renamed into place only on success, so
// a failure mid-transcript leaves the harp exactly as it was. Without that,
// hasCanonicalTranscript's presence-only guard would treat a partial file as a
// complete one forever, silently masking the original failure on every later
// call for this harp instead of allowing a genuine retry.
func ConvertVendorTranscript(ctx context.Context, e sessions.Entry) (converted bool, err error) {
	return convertVendorTranscript(ctx, e, false)
}

// RefreshVendorTranscript re-converts e's vendor-native transcript even when a
// canonical one already exists, atomically replacing it.
//
// It is for a session that is STILL BEING WRITTEN. ConvertVendorTranscript's
// presence-only idempotency is correct for a finished session — a transcript
// that cannot change need never be re-read — and is exactly wrong for a live
// one: converting at 12:00 and reading back at 14:00 yields a transcript
// silently frozen at noon, which is worse than the failure it replaced because
// it is indistinguishable from a complete one.
//
// The cost this accepts is the whole vendor transcript being re-read and
// re-written on each call, since no adapter can resume from an offset
// (vendorreader.VendorAdapter). That is why this is a separate verb rather than
// the default: callers that know their session is finished should not pay it,
// and a sweep across an index must not.
func RefreshVendorTranscript(ctx context.Context, e sessions.Entry) (converted bool, err error) {
	return convertVendorTranscript(ctx, e, true)
}

// convertVendorTranscript is the shared body of ConvertVendorTranscript and
// RefreshVendorTranscript. refresh drops the already-captured guard and lets the
// resulting file replace an existing canonical transcript; everything else —
// adapter selection, the refusal level, degrade-to-partial, the temp-then-rename
// write — is identical, so the two verbs cannot drift into two conversions.
//
// The rebuild is HARP-LIFETIME, not single-file: e.Rotations (sessions.Entry's
// lineage of displaced bindings — see sessions.Manager.BindSession) names every
// vendor transcript a /clear has rotated this harp past, oldest first, and the
// canonical file this produces is the concatenation of each rotation's cached
// segment (paths.ResolveHarpSegmentPath, converted once and reused thereafter —
// see appendRotationSegment) followed by the live binding's own conversion. A
// pre-lineage build (no Rotations recorded) degrades to exactly the old
// single-file behavior. Without this, a harp that had ever been /clear'd lost
// everything said before the clear the moment the canonical transcript was
// (re)built: the vendor file naming that conversation was still on disk, but
// nothing pointed at it anymore.
func convertVendorTranscript(ctx context.Context, e sessions.Entry, refresh bool) (converted bool, err error) {
	reg, ok := vendorReaderRegistry[e.Backend]
	if !ok || e.HarpName == "" {
		return false, nil
	}
	if !refresh && hasCanonicalTranscript(e.HarpName) {
		return false, nil
	}
	liveSrc, liveOK := reg.locate(ctx, e)
	if !liveOK && len(e.Rotations) == 0 {
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

	// Convert into a temporary sibling, never straight at the canonical file.
	//
	// Two distinct hazards need this, and neither is hypothetical. Convert can
	// fail AFTER recording some lines (a bad byte partway through a large
	// transcript), and a Recorder creates its file on the first SUCCESSFUL
	// Record — so writing in place leaves a real, non-empty, PARTIAL canonical
	// file, which hasCanonicalTranscript's presence-only guard then treats as a
	// complete one on every future call, permanently masking the failure. And a
	// refresh writing in place would APPEND a second full copy of the vendor
	// transcript onto the existing one, since a Recorder appends and no adapter
	// resumes from an offset. Renaming only on success answers both: the harp
	// keeps whatever it had until a complete replacement exists.
	//
	// os.Rename replacing dest is also what correctly handles dest being a
	// SYMLINK: linkTranscriptIntoHarpDir (sessions.BindSession's best-effort
	// convenience link) may have left transcript.jsonl pointing at the live
	// vendor file rather than a regular file. rename(2) replaces whatever
	// directory entry is at the destination path atomically, symlink or not —
	// it does not follow it — so this needs no special-casing for that case.
	dest, derr := canonicalDestination(e.HarpName, refresh)
	if derr != nil {
		return true, fmt.Errorf("resolve canonical transcript path for %s: %w", e.HarpName, derr)
	}
	tmp := dest + ".rebuild"
	// A previous crash between write and rename leaves this behind; it carries
	// no information (every source is re-read in full) and would otherwise be
	// appended to.
	_ = os.Remove(tmp)

	for _, rot := range e.Rotations {
		if werr := appendRotationSegment(ctx, adapter, e, rot, tmp); werr != nil {
			_ = os.Remove(tmp)
			return true, werr
		}
	}

	if liveOK {
		rec, rerr := transcript.NewRecorder(e.HarpName, e.Backend, transcript.WithPath(tmp))
		if rerr != nil {
			_ = os.Remove(tmp)
			return true, fmt.Errorf("open recorder for %s: %w", e.HarpName, rerr)
		}
		cerr := adapter.Convert(ctx, rec, liveSrc)
		_ = rec.Close()
		if cerr != nil {
			// Best-effort: removal failing is not itself reported, since the
			// conversion error is already the actionable fact.
			_ = os.Remove(tmp)
			return true, fmt.Errorf("convert %s transcript for %s: %w", e.Backend, e.HarpName, cerr)
		}
	}

	// Convert succeeding is NOT the same fact as bytes landing on disk.
	// transcript.Recorder only creates its canonical file on the FIRST
	// SUCCESSFUL Record, so a live Convert that (legitimately, per
	// vendorreader.VendorAdapter's own degrade-to-partial contract) wrote zero
	// entries — combined with no rotation contributing a segment either —
	// leaves no tmp file at all.
	info, serr := os.Stat(tmp)
	if serr != nil || info.Size() == 0 {
		_ = os.Remove(tmp)
		// A harp with NO recorded rotations degrading to nothing is the
		// ordinary single-file "nothing to do" outcome (unchanged from before
		// rotation lineage existed): reporting it as an error would turn every
		// legitimately-empty vendor transcript into a false alarm. A harp WITH
		// rotations is different: the index itself testifies that a /clear
		// happened and a pre-clear conversation once existed, so a rebuild
		// that recovers NOTHING from any segment or the live transcript is
		// this project's characteristic silent-no-op failure — exit clean,
		// report nothing, and the pre-clear conversation is gone for good.
		// That must be surfaced, not folded into the quiet "nothing to do"
		// case (see appendRotationSegment's per-rotation stderr diagnostics
		// for which files were actually missing).
		if len(e.Rotations) > 0 {
			return true, fmt.Errorf("rebuild canonical transcript for %s: %d rotation(s) recorded in this harp's lineage, but no bytes could be recovered from any of them or from the live transcript — every vendor file in the lineage is gone or produced nothing (see stderr for which)", e.HarpName, len(e.Rotations))
		}
		return false, nil
	}
	if rerr := os.Rename(tmp, dest); rerr != nil {
		_ = os.Remove(tmp)
		return true, fmt.Errorf("install canonical transcript for %s: %w", e.HarpName, rerr)
	}
	return true, nil
}

// appendRotationSegment ensures a cached canonical segment exists for one
// displaced binding in e's rotation lineage — converting it once via adapter
// when no cache is present, reusing the cached file otherwise (paths.
// ResolveHarpSegmentPath) — and appends its bytes onto tmp, the harp-lifetime
// canonical rebuild in progress.
//
// A rotation whose vendor file is gone (rotated-away files can be reaped by
// the vendor, the OS, or a user) is SKIPPED, not a failure: one missing
// segment must never fail the whole rebuild, which is why this reports
// through clidiag rather than returning an error for that case. Only a
// genuine I/O failure while converting or caching a segment that DOES exist
// returns an error.
func appendRotationSegment(ctx context.Context, adapter vendorreader.VendorAdapter, e sessions.Entry, rot sessions.Rotation, tmp string) error {
	segPath, perr := paths.ResolveHarpSegmentPath(e.HarpName, rot.SessionID)
	if perr != nil {
		return fmt.Errorf("resolve segment path for %s/%s: %w", e.HarpName, rot.SessionID, perr)
	}

	if _, statErr := os.Stat(segPath); statErr != nil {
		if !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("stat cached segment %s: %w", segPath, statErr)
		}
		// Not cached yet: convert this rotation's vendor file once now.
		if rot.TranscriptPath == "" {
			clidiag.Warn("ctxloom", "rebuild %s: rotation %s recorded no transcript path; skipping this segment of the lineage", e.HarpName, rot.SessionID)
			return nil
		}
		if _, verr := os.Stat(rot.TranscriptPath); verr != nil {
			clidiag.Warn("ctxloom", "rebuild %s: rotation %s's vendor transcript %s is gone (%v); skipping this segment of the lineage", e.HarpName, rot.SessionID, rot.TranscriptPath, verr)
			return nil
		}
		if mkErr := os.MkdirAll(filepath.Dir(segPath), 0o755); mkErr != nil {
			return fmt.Errorf("create segments dir for %s: %w", e.HarpName, mkErr)
		}
		segTmp := segPath + ".rebuild"
		_ = os.Remove(segTmp)
		rec, rerr := transcript.NewRecorder(e.HarpName, e.Backend, transcript.WithPath(segTmp))
		if rerr != nil {
			return fmt.Errorf("open segment recorder for %s/%s: %w", e.HarpName, rot.SessionID, rerr)
		}
		cerr := adapter.Convert(ctx, rec, rot.TranscriptPath)
		_ = rec.Close()
		if cerr != nil {
			_ = os.Remove(segTmp)
			return fmt.Errorf("convert rotation %s transcript for %s: %w", rot.SessionID, e.HarpName, cerr)
		}
		if info, serr := os.Stat(segTmp); serr != nil || info.Size() == 0 {
			// Zero events converted: a legitimate degrade-to-partial outcome
			// for THIS segment (vendorreader.VendorAdapter's contract), not a
			// failure — nothing to cache, nothing to append.
			_ = os.Remove(segTmp)
			return nil
		}
		if rerr := os.Rename(segTmp, segPath); rerr != nil {
			_ = os.Remove(segTmp)
			return fmt.Errorf("install cached segment for %s/%s: %w", e.HarpName, rot.SessionID, rerr)
		}
	}

	return appendFileBytes(tmp, segPath)
}

// appendFileBytes copies src's full contents onto the end of dst, creating
// dst if it doesn't exist yet. Used to concatenate a harp's cached rotation
// segments (each already in canonical JSONL form) onto the in-progress
// harp-lifetime rebuild, ahead of the live binding's own conversion.
func appendFileBytes(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

// canonicalDestination is the file a conversion for harp should end up at.
//
// A refresh must land on the transcript that is ALREADY THERE, resolved name and
// all: a pre-rename session carries only the legacy transcript.acp.jsonl, and
// writing the replacement under the current name would leave BOTH on disk — the
// duplication the whole temp-then-rename dance exists to prevent, arrived at from
// the other direction. A first conversion has nothing to resolve and takes the
// current name.
func canonicalDestination(harp string, refresh bool) (string, error) {
	if refresh {
		if p, err := paths.ResolveHarpCanonicalTranscriptPath(harp); err == nil && p != "" {
			return p, nil
		}
	}
	return paths.HarpCanonicalTranscriptPath(harp)
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

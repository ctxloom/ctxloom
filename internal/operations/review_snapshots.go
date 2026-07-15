package operations

import (
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Approved-content snapshots.
//
// Every approval countersigns an item's raw bytes, and its distilled bytes too
// when a distilled form exists; this store keeps the BYTES those signatures
// cover, content-addressed under .ctxloom/cache/trust/objects/<hash>, so that
// when an upstream edit returns the item to pending, `ctxloom review` can show
// a unified diff against what the human previously approved instead of a full
// re-read.
//
// The store is pure cache with cache semantics end to end: writes are
// best-effort (a failure warns and never fails the approval — the
// countersignature is the authority), a missing snapshot degrades review to a
// full-content display, and there is no pruning this slice — objects are tiny
// (fragment/command text) and superseded entries are simply never read again; a
// pruning pass can walk the countersignature stores' live approvals later if
// it ever matters.

// snapshotFilename maps a recorded content hash ("sha256:<hex>") to its
// object filename. The algorithm prefix is folded into the name rather than
// stripped so a future second algorithm cannot collide; ':' is replaced
// because it is not portable in filenames (Windows, and it reads as a path
// separator in tooling).
func snapshotFilename(hash string) string {
	return strings.ReplaceAll(hash, ":", "-")
}

// writeTrustSnapshot stores content under its recorded hash. Best-effort: any
// failure warns and returns (the acceptance already persisted; a lost
// snapshot only costs a future diff). Empty hashes/content are skipped — an
// empty slot means "no such form", not "empty bytes".
func writeTrustSnapshot(fs afero.Fs, baseDir, hash string, content []byte) {
	if hash == "" || len(content) == 0 {
		return
	}
	dir := paths.TrustObjectsPath(baseDir)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "could not create trust snapshot dir: %v", err)
		return
	}
	path := filepath.Join(dir, snapshotFilename(hash))
	if err := afero.WriteFile(fs, path, content, 0o644); err != nil {
		clidiag.Warn("ctxloom", "could not write trust snapshot %s: %v", snapshotFilename(hash), err)
	}
}

// readTrustSnapshot returns the accepted bytes recorded under hash, or
// ("", false) when no snapshot exists (never an error: a missing snapshot is
// the documented degrade — full-content display).
func readTrustSnapshot(fs afero.Fs, baseDir, hash string) (string, bool) {
	if hash == "" {
		return "", false
	}
	data, err := afero.ReadFile(fs, filepath.Join(paths.TrustObjectsPath(baseDir), snapshotFilename(hash)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// snapshotAcceptedItemContent writes the accepted-content snapshots for an
// acceptance that was just recorded: the raw and (when one exists) distilled
// bytes of a fragment/command, or a skill's rendered per-file tree listing
// (renderSkillSurface — a skill has no distilled form), each stored under the
// hash the acceptance recorded for that form (rawHash / distilledHash — never
// recomputed here, so snapshot keys cannot drift from the store). It is the
// one shared snapshot writer under BOTH acceptance surfaces — the `ctxloom
// review` porcelain and the `ctxloom trust` plumbing route through
// SetItemTrust, which calls this after the store write succeeds.
//
// Executable kinds (mcp, hooks) are deliberately not snapshotted: their
// recorded hash covers a canonical JSON encoding, and review always renders
// their full executable surface (command/args/env) rather than a diff — the
// surface is small enough that a diff adds nothing. A skill is different: it
// is a tree of files precisely so it CAN be diffed file-by-file, so it is
// snapshotted like fragments/commands (IsContent() includes trust.KindSkill).
//
// Best-effort by construction (every write path warns instead of failing).
func snapshotAcceptedItemContent(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string, fs afero.Fs, rawHash, distilledHash string) {
	if !tRef.Kind.IsContent() {
		return
	}
	bundle, err := loader.Load(loadRef)
	if err != nil {
		clidiag.Warn("ctxloom", "could not snapshot accepted content for %q: %v", tRef.Key(), err)
		return
	}
	raw, distilled, ok := itemContentPair(bundle, tRef)
	if !ok {
		clidiag.Warn("ctxloom", "could not snapshot accepted content: %q not found in %q", tRef.Name, loadRef)
		return
	}
	baseDir := getBaseDir(cfg)
	fs = getFS(fs)
	writeTrustSnapshot(fs, baseDir, rawHash, []byte(raw))
	if distilledHash != "" {
		writeTrustSnapshot(fs, baseDir, distilledHash, []byte(distilled))
	}
}

// itemContentPair returns a content item's (raw, distilled) text. distilled is
// "" when the item has no distilled form (or suppresses it via NoDistill —
// EffectiveContent(true) then returns the raw text, which the comparison
// catches). ok=false for executables and missing items.
func itemContentPair(bundle *bundles.Bundle, tRef trust.Ref) (raw, distilled string, ok bool) {
	switch tRef.Kind {
	case trust.KindFragment:
		frag, found := bundle.Fragments[tRef.Name]
		if !found {
			return "", "", false
		}
		raw, distilled = formPair(frag.EffectiveContent)
		return raw, distilled, true
	case trust.KindPrompt:
		command, found := bundle.Commands[tRef.Name]
		if !found {
			return "", "", false
		}
		raw, distilled = formPair(command.EffectiveContent)
		return raw, distilled, true
	case trust.KindSkill:
		skill, found := bundle.Skills[tRef.Name]
		if !found {
			return "", "", false
		}
		// A skill has no distilled form: the snapshot is always the same
		// rendered tree-listing text `ctxloom review` itself displays
		// (renderSkillSurface), so a later diff shows exactly which file(s)
		// changed — the per-file-diff contract skills are stored as a tree
		// to get.
		return renderSkillSurface(skill), "", true
	default:
		return "", "", false
	}
}

// formPair extracts (raw, distilled) text from the shared EffectiveContent
// primitive: preferDistilled=false always yields the raw form;
// preferDistilled=true yields the distilled form exactly when one exists
// (identical output means there is none).
func formPair(effective func(bool) string) (raw, distilled string) {
	raw = effective(false)
	if d := effective(true); d != raw {
		return raw, d
	}
	return raw, ""
}

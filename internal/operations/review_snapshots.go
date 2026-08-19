package operations

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Approved-content snapshots.
//
// Every approval countersigns an item's raw bytes, and its distilled bytes too
// when a distilled form exists; this store keeps the BYTES those signatures
// cover, content-addressed under .ctxloom/state/trust/objects/<hash>, so that
// when an upstream edit returns the item to pending, `ctxloom review` can show
// a unified diff against what the human previously approved instead of a full
// re-read.
//
// The store's WRITES are best-effort (a failure warns and never fails the
// approval — the countersignature is the authority) and a missing snapshot
// degrades review to a full-content display. That made it look like a cache,
// and it lived under cache/ until nothing turned out to rebuild it: these are
// the bytes as they were at the moment a human said yes, and no pull or
// re-derivation reconstructs them. It is state (paths.TrustObjectsPath).
//
// There is no pruning: objects are tiny (fragment/command text) and superseded
// entries are simply never read again; a pruning pass can walk the
// countersignature stores' live approvals later if it ever matters.

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
	dir := trustObjectsDir(fs, baseDir)
	if err := fs.MkdirAll(dir, 0o755); err != nil {
		clidiag.Warn("ctxloom", "could not create trust snapshot dir: %v", err)
		return
	}
	path := filepath.Join(dir, snapshotFilename(hash))
	// No AllowEmpty: len(content) == 0 already returned above.
	if err := iox.WriteFileAtomicFs(fs, path, content, 0o644); err != nil {
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
	data, err := afero.ReadFile(fs, filepath.Join(trustObjectsDir(fs, baseDir), snapshotFilename(hash)))
	if err != nil {
		return "", false
	}
	return string(data), true
}

// trustObjectsDir returns the snapshot directory, having first moved a legacy
// cache/trust/objects store into it. It is the ONE way either half of this
// store names its directory, so the migration cannot fire on the write path and
// not the read path — a project whose objects were still under cache/ would
// otherwise read as having no diff bases at all while happily writing new ones
// somewhere else.
//
// The cost of asking every time is one stat of a directory that does not exist
// in the ordinary case.
func trustObjectsDir(fs afero.Fs, baseDir string) string {
	current := paths.TrustObjectsPath(baseDir)
	migrateLegacyTrustObjects(fs, paths.LegacyTrustObjectsPath(baseDir), current)
	return current
}

// migrateLegacyTrustObjects performs the ONE-WAY, ONE-TIME move of the
// pre-relocation cache/trust/objects store to state/trust/objects. Four cases:
//
//   - no legacy directory — the ordinary case; nothing happens, nothing is said.
//   - legacy only — moved, with a stderr note naming both paths.
//   - BOTH exist — nothing moves and a warning names both plus which one is
//     live. The stores are content-addressed, so merging LOOKS free; it stops
//     being free the moment the two were written by builds that disagreed about
//     hashing, at which point a copy silently replaces the diff base an
//     approval actually covered.
//   - legacy is a SYMLINK (or any non-directory) — skipped with a warning,
//     never followed. Moving a link relocates whatever it points at, which may
//     be a directory somebody else is still using.
//
// Failure never propagates: this store is best-effort by construction (a lost
// snapshot costs a diff, not correctness), and an approval must not fail
// because a directory could not be moved.
func migrateLegacyTrustObjects(fs afero.Fs, legacy, current string) {
	info, err := lstatIfPossible(fs, legacy)
	switch {
	case err != nil && errors.Is(err, iofs.ErrNotExist):
		return
	case err != nil:
		clidiag.Warn("ctxloom", "cannot inspect the legacy trust snapshot store %s (%v); skipping the one-time move to %s — check it by hand", legacy, err, current)
		return
	case info.Mode()&os.ModeSymlink != 0:
		clidiag.Warn("ctxloom", "the legacy trust snapshot store %s is a SYMLINK; refusing to follow it — ctxloom now reads %s, so move the contents there yourself if that link points at snapshots you want", legacy, current)
		return
	case !info.IsDir():
		clidiag.Warn("ctxloom", "the legacy trust snapshot store %s is not a directory; leaving it alone — ctxloom now reads %s", legacy, current)
		return
	}

	switch exists, statErr := afero.DirExists(fs, current); {
	case statErr != nil:
		clidiag.Warn("ctxloom", "cannot inspect the trust snapshot store %s (%v); skipping the one-time move from %s", current, statErr, legacy)
		return
	case exists:
		clidiag.Warn("ctxloom", "two trust snapshot stores exist: the legacy %s and the current %s. ctxloom reads and writes the CURRENT one — %s — and moved nothing; merge or delete the legacy directory yourself", legacy, current, current)
		return
	}

	if err := moveTrustObjects(fs, legacy, current); err != nil {
		clidiag.Warn("ctxloom", "could not move the legacy trust snapshot store %s to %s (%v); nothing was deleted — move it by hand, or delete it and accept full-content review for items approved before now", legacy, current, err)
		return
	}
	clidiag.Warn("ctxloom", "moved the approved-content snapshot store from %s to %s (one time, contents preserved): nothing rebuilds these snapshots, so they belong in state/ rather than in a cache a user is invited to wipe", legacy, current)
}

// lstatIfPossible stats path WITHOUT following a final symlink where the
// filesystem supports it (afero.OsFs does; MemMapFs has no symlinks to
// confuse). A plain Stat would report a symlinked legacy store as the directory
// it points at, and the move would then relocate that directory.
func lstatIfPossible(fs afero.Fs, path string) (os.FileInfo, error) {
	if lstater, ok := fs.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(path)
		return info, err
	}
	return fs.Stat(path)
}

// moveTrustObjects relocates src to dst, preferring a rename and falling back
// to copy-then-remove when the two are on different filesystems (rename returns
// EXDEV, which no amount of retrying fixes — and cache/ vs state/ on separate
// mounts is a shape a user can produce with one symlink of .ctxloom/cache).
// The fallback removes the source only after every byte has landed, so a failed
// copy leaves the original intact.
func moveTrustObjects(fs afero.Fs, src, dst string) error {
	if err := fs.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	if err := fs.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTrustObjects(fs, src, dst); err != nil {
		return err
	}
	if err := fs.RemoveAll(src); err != nil {
		return fmt.Errorf("remove the emptied %s after copying it to %s: %w", src, dst, err)
	}
	return nil
}

// copyTrustObjects duplicates the src subtree at dst, preserving each file's
// mode. The store is flat today (one file per content hash); the walk covers a
// nested one anyway rather than silently dropping whatever it did not expect.
func copyTrustObjects(fs afero.Fs, src, dst string) error {
	return afero.Walk(fs, src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return fs.MkdirAll(target, info.Mode().Perm())
		}
		data, readErr := afero.ReadFile(fs, path)
		if readErr != nil {
			return readErr
		}
		// iox chmods to info.Mode().Perm() exactly on every write, closing the
		// same latent stale-mode gap the content/archive and remote/pull
		// migrations closed: afero.WriteFile only applies mode at creation, so
		// a copy onto a pre-existing target used to keep the target's OLD mode.
		return iox.WriteFileAtomicFs(fs, target, data, info.Mode().Perm())
	})
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
func snapshotAcceptedItemContent(cfg *config.Config, cat bundles.Catalog, tRef trust.Ref, key trust.BundleKey, fs afero.Fs, rawHash, distilledHash string) {
	if !tRef.Kind.IsContent() {
		return
	}
	bundle, err := cat.LoadKey(key)
	if err != nil {
		clidiag.Warn("ctxloom", "could not snapshot accepted content for %q: %v", tRef.Key(), err)
		return
	}
	// The SET's filesystem — NOT fs: fs is the snapshot store, while a skill's
	// manifest must be derived from the same filesystem the bundle tree was
	// resolved on, or the snapshot would describe a different tree than the
	// hash it is filed under.
	raw, distilled, ok := itemContentPair(cat.FS(), bundle, tRef)
	if !ok {
		clidiag.Warn("ctxloom", "could not snapshot accepted content: %q not found in %q", tRef.Name, key)
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
func itemContentPair(bundleFS afero.Fs, bundle *bundles.Bundle, tRef trust.Ref) (raw, distilled string, ok bool) {
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
		// to get. The EFFECTIVE manifest is used so an unsynced skill
		// snapshots its real tree rather than an empty listing.
		skillDir, dirErr := bundle.SkillPreimageDir(skill)
		if dirErr != nil {
			return "", "", false
		}
		manifest, merr := skill.EffectiveManifest(bundleFS, skillDir, tRef.Name)
		if merr != nil {
			return "", "", false
		}
		return renderSkillSurface(manifest), "", true
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

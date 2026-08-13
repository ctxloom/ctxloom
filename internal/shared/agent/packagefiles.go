package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/spf13/afero"
)

// This file generalizes WriteManagedCommandFiles's manifest/traversal/cleanup
// mechanics from a SINGLE-FILE writer into a TREE (package) writer: a command
// is the degenerate case of a package with exactly one file. WriteManagedCommandFiles
// (commandfiles.go) is now a thin adapter over WriteManagedPackageFiles — this is
// the shared seam the skill package delivery (SkillExport, ManagedSkillPackagesDelivery)
// and every future per-engine skill writer build on (skill-command-split.plan.md §3.4).

// PackageFile is one file within a rendered package: its path relative to the
// managed dir, its content, and its POSIX file mode. Mode 0 defaults to 0644 —
// WriteManagedPackageFiles' historical single-file default — so callers that
// don't care about the exec bit (plain text files) can leave it zero.
type PackageFile struct {
	RelPath string
	Content []byte
	Mode    os.FileMode
}

// defaultPackageFileMode is applied when a PackageFile's Mode is the zero
// value, matching the historical WriteManagedCommandFiles hardcoded 0644.
const defaultPackageFileMode os.FileMode = 0644

// preparedItem is one enabled, path-safe item after render — files paired
// with their already-validated destination paths (paths[i] is the resolved,
// dir-confined path for files[i]).
type preparedItem struct {
	name  string
	files []PackageFile
}

// WriteManagedPackageFiles is the manifest-scoped TREE writer shared by every
// per-agent package writer (a command-file writer with exactly one rendered
// file per item, and a skill-package writer with SKILL.md plus its sibling
// files). dir is shared territory with user-authored files, and can ALSO be
// shared with a co-located surface's own managed set (kiro writes commands and
// skills into one native directory — see internal/shared/ledger's package
// doc), so it is never wiped wholesale: ctxloom tracks every file it wrote in
// a manifest (the shared managed-content ledger, scoped to this surface).
//
// items is the caller's list of exportable things (CommandExport, SkillExport,
// …); enabled and itemName pick generic accessors off each item (kept as
// closures, not an interface, so neither export type needs a method just for
// this); render maps ONE enabled item to every file it materializes — a single
// entry for a command, SKILL.md plus every sibling file for a skill package.
// Every rendered path is validated with SafeCommandRelPath (both command/skill
// names and manifest lines can originate in bundle content, potentially
// remote): a path that escapes dir is rejected with a warning, never followed.
//
// RENDER-THEN-SWAP, not delete-then-rerender. Every enabled item is rendered
// and path-validated ENTIRELY OFF the live tree first (nothing under dir is
// touched); the complete new file set is then written into a temp sibling of
// dir and, once fully materialized there, moved into place file-by-file with
// atomic renames. Only after every new file is safely live are the
// now-unwanted previously-tracked files (an item that got disabled) removed.
// This ordering — validate, render, swap, THEN clean up stale entries — is
// the fix for the historical bug: the old writer deleted this surface's
// entire previously-tracked set FIRST and rendered second, so any failure
// between the two left the surface gutted while still reporting success (the
// project's signature silent no-op), and even on the happy path a concurrent
// reader could observe a file the ledger still claims as gone. See
// packagefiles_race_test.go and packagefiles_swap_test.go for the tests this
// ordering exists to pass.
//
// An item's files are validated (path-safety) as a whole BEFORE any of them is
// written, so a single unsafe path in a multi-file package skips the WHOLE
// item rather than leaving a partial tree on disk — the silent-no-op /
// partial-materialize discipline this codebase holds writers to. An item whose
// render() call itself returns an error is different in kind from an
// unsafe-path skip (that is bundle content being deliberately declined, not a
// failure): a render error aborts THIS ENTIRE CALL, propagating the error and
// leaving dir exactly as it was — nothing has been written to dir yet at that
// point, so "abort" costs nothing and "warn and keep going" is exactly how
// this writer used to silently gut a surface one failing item at a time.
//
// dir itself is only created when at least one file is written; the manifest
// is (re)written only when at least one file was written. When cleanup leaves
// a now-empty subdirectory behind (a package's scripts/assets dir with every
// file removed), it is pruned bottom-up on a best-effort basis so a disabled
// skill leaves no debris.
func WriteManagedPackageFiles[T any](
	fs afero.Fs,
	dir string,
	surface ledger.Surface,
	items []T,
	enabled func(T) bool,
	itemName func(T) string,
	render func(T) ([]PackageFile, error),
	opts ...ManagedWriteOption,
) error {
	o := &managedWriteOptions{}
	for _, opt := range opts {
		opt(o)
	}
	led := ledger.Ledger{FS: fs, Dir: dir, Warn: Warn}

	// Read what this surface currently claims BEFORE anything else — read-only,
	// nothing destructive yet. Ledger entries are data, not trusted paths: a
	// doctored line ("../x", absolute) is caught by SafeCommandRelPath wherever
	// it is later consumed (the guard below, and the stale-cleanup in phase 4),
	// never followed blindly.
	previous, err := led.Read(surface)
	if err != nil {
		return err
	}

	// PHASE 1 — render + validate every enabled item OFF the live tree. Not one
	// byte under dir is touched in this phase; a render() failure returns
	// immediately, before dir has seen any change at all.
	var enabledCount int
	var prepared []preparedItem
	for _, item := range items {
		if !enabled(item) {
			continue
		}
		enabledCount++
		name := itemName(item)
		// Reject absolute/traversal names outright before any path is derived
		// from them. Nested names without traversal ("group/cmd") remain
		// allowed; how they map to paths is the renderer's choice. This is a
		// content-hygiene skip (bundle content can be remote/untrusted), not a
		// render failure, so it warns and moves on rather than aborting the
		// whole call.
		if _, ok := SafeCommandRelPath(dir, name); !ok {
			Warn("skipping package %q: name is not a relative path inside %s", name, dir)
			continue
		}
		files, err := render(item)
		if err != nil {
			return fmt.Errorf("write managed package files %s: package %q: render failed: %w", dir, name, err)
		}
		safe := true
		for _, f := range files {
			if _, ok := SafeCommandRelPath(dir, f.RelPath); !ok {
				Warn("skipping package %q: rendered path %q is not a relative path inside %s", name, f.RelPath, dir)
				safe = false
				break
			}
		}
		if !safe {
			continue
		}
		prepared = append(prepared, preparedItem{name: name, files: files})
	}

	var expectedFileCount int
	for _, p := range prepared {
		expectedFileCount += len(p.files)
	}
	// Empty-render guard. Fires only when ALL THREE hold: the caller asked for
	// content (enabledCount > 0 — an empty/all-disabled items list is a
	// legitimate revert, handled below), rendering produced not one legitimate
	// file, AND there is existing content this would otherwise destroy
	// (previous, from the ledger, is non-empty). Without the third condition
	// this would reject the ordinary "every enabled item's path happens to be
	// unsafe" case even on a first-ever materialize with nothing at stake
	// (see TestWriteManagedPackageFiles_UnsafeItemPathSkipsWholeItem); without
	// the first, it would reject the ordinary "user disabled the last item"
	// cleanup call (see TestWriteManagedPackageFiles_CleanupPreservesForeignFiles).
	// What it exists to catch is exactly the shape that gutted a surface
	// before: content used to be here, the caller still wants content here,
	// and this call is about to produce none.
	if enabledCount > 0 && expectedFileCount == 0 && len(previous) > 0 {
		return fmt.Errorf("write managed package files %s: %d enabled item(s) rendered zero files while %d previously-managed file(s) exist; refusing to touch the existing surface", dir, enabledCount, len(previous))
	}

	if expectedFileCount == 0 {
		// Legitimate empty target: no enabled items (or none survived
		// path-safety validation with nothing at stake — the guard above
		// already ruled out the destructive version of that case). Nothing to
		// swap in; just revert this surface's previously-tracked set.
		return revertManagedSurface(fs, dir, surface, previous, led)
	}

	// PHASE 2 — render the complete new file set into a temp SIBLING of dir
	// (same parent, so phase 3's renames stay on one volume), using ordinary
	// writes. These files are invisible to any reader of dir until phase 3, so
	// a failure here (disk full mid-item, an I/O error) still leaves dir
	// completely untouched — the same guarantee a render() error gets in phase
	// 1, extended to the write itself.
	tempDir, err := afero.TempDir(fs, filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return fmt.Errorf("write managed package files %s: create temp render tree: %w", dir, err)
	}
	tempDirLive := true
	defer func() {
		if tempDirLive {
			_ = fs.RemoveAll(tempDir)
		}
	}()

	var written []string
	for _, p := range prepared {
		for _, f := range p.files {
			// Cross-scope dedup ("home/global wins"), per file: when writing
			// into a NON-home dir, skip a file byte-identical to the
			// same-named one already in the global dir — checked against the
			// LIVE home dir (untouched by this call), so this lookup is safe
			// to do before the swap. See WriteManagedCommandFiles for the
			// full rationale.
			if o.dedupHomeDir != "" && filepath.Clean(dir) != filepath.Clean(o.dedupHomeDir) {
				if homePath, ok := SafeCommandRelPath(o.dedupHomeDir, f.RelPath); ok {
					if existing, rerr := afero.ReadFile(fs, homePath); rerr == nil && bytes.Equal(existing, f.Content) {
						continue
					}
				}
			}
			tempPath := filepath.Join(tempDir, f.RelPath)
			if err := fs.MkdirAll(filepath.Dir(tempPath), 0755); err != nil {
				return fmt.Errorf("write managed package files %s: package %q: create temp subdir: %w", dir, p.name, err)
			}
			mode := f.Mode
			if mode == 0 {
				mode = defaultPackageFileMode
			}
			if err := afero.WriteFile(fs, tempPath, f.Content, mode); err != nil {
				return fmt.Errorf("write managed package files %s: package %q: render %s: %w", dir, p.name, f.RelPath, err)
			}
			// afero.WriteFile's mode argument only applies at file CREATION
			// (via OpenFile's perm); tempPath was just created fresh, so this
			// is normally redundant, but re-assert explicitly in case a
			// backend doesn't honor perm-on-create — a warn-only best effort,
			// same as the historical re-assert. A chmod failure on a
			// brand-new temp file is not the missing-content defect this
			// rewrite targets, so it does not abort the swap.
			if err := fs.Chmod(tempPath, mode); err != nil {
				Warn("package %q: chmod %s to %s failed: %v", p.name, f.RelPath, mode, err)
			}
			written = append(written, f.RelPath)
		}
	}

	if len(written) == 0 {
		// Every file was skipped by the home-dir dedup ("home wins") —
		// legitimate, not a failure: nothing new to place, revert this
		// surface's previous set exactly like the intentional-empty path.
		return revertManagedSurface(fs, dir, surface, previous, led)
	}

	// PHASE 3 — swap. Each fully-rendered temp file moves into dir at its
	// final path with ONE fs.Rename: a single atomic replace, not an
	// unlink-then-create, so a concurrent reader of that path sees either the
	// complete old content or the complete new content and NEVER an absence.
	//
	// This is the file-scoped realization of "rename old → aside, rename temp
	// → live, remove aside" — a whole-DIRECTORY version of that dance (renaming
	// dir itself aside and a fully-assembled temp tree into its place) was
	// ruled out on reading the actual call sites: dir is shared territory a
	// bare directory swap would destroy. kiro writes its commands surface AND
	// its skills surface into the SAME native directory (two ledger surfaces,
	// one dir — internal/kiro/skillfiles.go), and a hand-authored file can sit
	// right beside managed content in any engine's dir. Swapping dir itself
	// would evict the co-located surface's files and any user content in the
	// same breath as this call's own content. Per-file rename against a temp
	// tree that is a sibling of dir (same parent, same volume) gets the
	// identical atomicity guarantee at the granularity dir's sharing contract
	// actually allows, without an "aside" step: replacing an existing FILE via
	// rename(2) is already a single atomic substitution — the aside dance is
	// only needed to replace a non-empty DIRECTORY, which this design never
	// attempts.
	//
	// REMAINING WINDOW: between the first rename in this loop and the last, a
	// concurrent reader can observe a MIX of old and new content across
	// DIFFERENT files of the same call (e.g. one skill's new SKILL.md already
	// swapped in while a second skill's old scripts/run.sh has not been
	// reached yet). What it can never observe, at any point in this loop, is a
	// tracked path that is simply ABSENT — every rename is a single
	// substitution of one complete version for another. That makes this a
	// staleness window, not a data-loss window, and it is strictly smaller
	// than the old delete-everything-then-rerender window: the old code's gap
	// could and did (taskloom dutiful-water) leave a tracked file gone
	// entirely, observable by any reader, for the full duration of the
	// re-render. A rename failing partway through this loop (possible in
	// principle — a permission or ENOSPC failure on the metadata update;
	// vanishingly unlikely in practice since tempDir and dir share a parent
	// and every rename is a pure metadata operation) stops the loop
	// immediately: whatever already swapped stays swapped at its new content,
	// whatever had not been reached yet stays at its old content, and the
	// error propagates WITHOUT running phase 4's stale-cleanup or writing the
	// ledger — so the ledger never claims ownership of a state that was not
	// actually reached.
	for _, relPath := range written {
		dst := filepath.Join(dir, relPath)
		src := filepath.Join(tempDir, relPath)
		if err := fs.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fmt.Errorf("write managed package files %s: create %s: %w", dir, filepath.Dir(dst), err)
		}
		if err := fs.Rename(src, dst); err != nil {
			return fmt.Errorf("write managed package files %s: swap %s into place: %w", dir, relPath, err)
		}
	}
	// The rename loop above drained every file phase 2 wrote; RemoveAll here
	// is a best-effort prune of the now-empty temp subdirectories it leaves
	// behind (mirroring pruneEmptyDirs' role for dir itself). The deferred
	// cleanup above would do the same on any earlier return; skip it here only
	// to avoid a second, redundant walk on the success path.
	tempDirLive = false
	_ = fs.RemoveAll(tempDir)

	// PHASE 4 — now that every new file is safely live, remove exactly the
	// previously-tracked files this call no longer wants (an item that was
	// disabled or renamed away). Safe only NOW: doing this before phase 3
	// landed is the historical bug (delete-then-rerender).
	newSet := make(map[string]bool, len(written))
	for _, r := range written {
		newSet[r] = true
	}
	var removedDirs []string
	for _, name := range previous {
		if newSet[name] {
			continue
		}
		path, ok := SafeCommandRelPath(dir, name)
		if !ok {
			Warn("skipping unsafe package ledger entry %q: not a relative path inside %s", name, dir)
			continue
		}
		_ = fs.Remove(path)
		if parent := filepath.Dir(path); parent != filepath.Clean(dir) {
			removedDirs = append(removedDirs, parent)
		}
	}
	if len(removedDirs) > 0 {
		pruneEmptyDirs(fs, dir, removedDirs)
	}

	// PHASE 5 — the ledger is updated LAST, once dir's on-disk contents exactly
	// match `written`, so the instant this call is observably persisted the
	// ledger and the live tree agree. Before this line dir may briefly hold
	// stale-but-still-PRESENT entries the OLD ledger already described (safe:
	// nothing has vanished — phase 4 only just removed them) or new files the
	// ledger doesn't mention yet (also safe: nothing anywhere keys off an
	// under-count the way the old code's over-eager delete keyed off nothing
	// at all).
	return led.Write(surface, written)
}

// revertManagedSurface reverts one surface to empty: removes exactly the
// previously manifest-tracked set (never a co-located surface's entries, never
// a foreign file) and clears the ledger. This is the legitimate "nothing
// enabled" / "everything deduped against home" path — sharing the removal
// mechanics WriteManagedPackageFiles' phase 4 also uses, factored out so the
// empty and non-empty branches don't duplicate the ledger-removal walk.
func revertManagedSurface(fs afero.Fs, dir string, surface ledger.Surface, previous []string, led ledger.Ledger) error {
	var removedDirs []string
	for _, name := range previous {
		path, ok := SafeCommandRelPath(dir, name)
		if !ok {
			Warn("skipping unsafe package ledger entry %q: not a relative path inside %s", name, dir)
			continue
		}
		_ = fs.Remove(path)
		if parent := filepath.Dir(path); parent != filepath.Clean(dir) {
			removedDirs = append(removedDirs, parent)
		}
	}
	if len(previous) > 0 {
		pruneEmptyDirs(fs, dir, removedDirs)
	}
	return led.Write(surface, nil)
}

// pruneEmptyDirs removes each directory in dirs that is now empty, deepest
// first, walking upward toward (but never including) root — a best-effort
// cleanup so a disabled multi-file package (a skill's scripts/ or assets/
// subdirectory with every tracked file removed) doesn't leave empty debris
// behind. Errors (directory not empty, already gone, permission) are ignored:
// this is tidiness, not correctness — the manifest-tracked FILES are what
// cleanup contracts on.
func pruneEmptyDirs(fs afero.Fs, root string, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	seen := make(map[string]bool, len(dirs))
	var uniq []string
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			uniq = append(uniq, d)
		}
	}
	// Deepest paths first so a child empties out before its parent is tried.
	sort.Slice(uniq, func(i, j int) bool { return len(uniq[i]) > len(uniq[j]) })
	rootClean := filepath.Clean(root)
	for _, d := range uniq {
		for cur := filepath.Clean(d); cur != rootClean && strings.HasPrefix(cur, rootClean); {
			empty, err := afero.IsEmpty(fs, cur)
			if err != nil || !empty {
				break
			}
			if err := fs.Remove(cur); err != nil {
				break
			}
			cur = filepath.Dir(cur)
		}
	}
}

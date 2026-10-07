package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
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

// writeManagedPackageFilesLocked is WriteManagedPackageFiles' body, run under
// the dir lock its exported wrapper (below) takes.
func writeManagedPackageFilesLocked[T any](
	files safefs.Root,
	dir string,
	surface ledger.Surface,
	items []T,
	enabled func(T) bool,
	itemName func(T) string,
	render func(T) ([]PackageFile, error),
	opts ...ManagedWriteOption,
) error {
	o := &managedWriteOptions{rename: safefs.Rename}
	for _, opt := range opts {
		opt(o)
	}
	fs := files.Fs
	led := ledger.Ledger{Root: files, Dir: dir, Warn: o.rep.Warnf}

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
	// byte under dir is touched in this phase, whatever happens: a render()
	// failure for one item is a per-item WARN-AND-SKIP, same as an unsafe
	// path, and NOT a whole-call abort — a caller-level content-validation
	// failure (e.g. a command with no body to render) is an expected,
	// recoverable per-item condition, tolerated so one bad item doesn't take
	// the rest of a delivery down with it. What must never
	// happen is EVERY enabled item failing while content used to exist here —
	// that is the empty-render guard below, evaluated once over the whole
	// batch rather than per item.
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
		// allowed; how they map to paths is the renderer's choice.
		if _, ok := SafeCommandRelPath(dir, name); !ok {
			o.rep.Warnf("skipping package %q: name is not a relative path inside %s", name, dir)
			continue
		}
		files, err := render(item)
		if err != nil {
			o.rep.Warnf("skipping package %q: render failed: %v", name, err)
			continue
		}
		safe := true
		for _, f := range files {
			if _, ok := SafeCommandRelPath(dir, f.RelPath); !ok {
				o.rep.Warnf("skipping package %q: rendered path %q is not a relative path inside %s", name, f.RelPath, dir)
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
		return revertManagedSurface(o.rep, fs, dir, surface, previous, led)
	}

	// PHASE 2 — render the complete new file set into a temp SIBLING of dir
	// (same parent, so phase 3's renames stay on one volume), using ordinary
	// writes. These files are invisible to any reader of dir until phase 3, so
	// a failure here (disk full mid-item, an I/O error) still leaves dir
	// completely untouched — the same guarantee a render() error gets in phase
	// 1, extended to the write itself.
	//
	// dir's PARENT must exist before a sibling of dir can be created in it —
	// on a first-ever delivery (nothing under, say, .claude/ yet) it does not.
	// The pre-rewrite writer got this for free: its first fs.MkdirAll(dir, …)
	// call (at the first successful write) is recursive and created every
	// missing ancestor, dir's parent included, in the same call that created
	// dir itself. This rewrite splits "create the parent chain" from "create
	// dir" across two different phases (temp-tree creation now needs the
	// parent BEFORE dir exists at all; phase 3's swap loop still creates dir
	// itself, lazily, on the first real rename) — the guarantee preserved
	// here is exactly the old code's "the parent chain always exists before
	// any write is attempted", narrowed to just the parent since dir itself
	// is intentionally still not created until content actually lands in it.
	if err := fs.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return fmt.Errorf("write managed package files %s: create parent dir: %w", dir, err)
	}
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
				o.rep.Warnf("package %q: chmod %s to %s failed: %v", p.name, f.RelPath, mode, err)
			}
			written = append(written, f.RelPath)
		}
	}

	if len(written) == 0 {
		// Every file was skipped by the home-dir dedup ("home wins") —
		// legitimate, not a failure: nothing new to place, revert this
		// surface's previous set exactly like the intentional-empty path.
		return revertManagedSurface(o.rep, fs, dir, surface, previous, led)
	}

	// PHASE 3 — swap; see swapIntoPlace. A failure stops here WITHOUT running
	// phase 4's stale-cleanup or writing the ledger, so the ledger never
	// claims ownership of a state that was not actually reached.
	if err := swapIntoPlace(o, fs, dir, tempDir, written); err != nil {
		return err
	}
	// swapIntoPlace moved every rendered file that differed from live; this
	// RemoveAll drops the identical copies it left behind and the temp
	// subdirectories (mirroring pruneEmptyDirs' role for dir itself). The deferred
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
			o.rep.Warnf("skipping unsafe package ledger entry %q: not a relative path inside %s", name, dir)
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

// swapIntoPlace moves each rendered temp file into dir at its final path with
// ONE rename — a substitution, never an unlink-then-create — EXCEPT where the
// live file is already identical to the rendered one (see liveFileMatches),
// which is left untouched.
//
// The skip is a correctness property, not an optimisation. A replace is only
// atomic for a concurrent reader where rename(2) is: on Windows os.Rename is
// MoveFileEx(MOVEFILE_REPLACE_EXISTING), and a reader looking the path up
// while it runs can find it missing or be refused. Re-delivering an unchanged
// package — every session start — therefore must not replace anything (see
// TestWriteManagedPackageFiles_RedeliveryNeverReplacesAnUnchangedLiveFile).
// A file whose content or mode really changed still goes through that
// replace, so on Windows it alone carries a brief window.
//
// Files are swapped one by one, so between the first rename and the last a
// reader can see a MIX of old and new versions across different files of one
// call. dir is never swapped wholesale: it is shared territory — two ledger
// surfaces may share one native directory, and hand-authored files can sit
// beside managed ones — so a directory-level swap would evict both. A rename
// failure stops the loop: what already swapped stays new, the rest stays old.
func swapIntoPlace(o *managedWriteOptions, fs afero.Fs, dir, tempDir string, written []string) error {
	for _, relPath := range written {
		dst := filepath.Join(dir, relPath)
		src := filepath.Join(tempDir, relPath)
		if liveFileMatches(fs, src, dst) {
			continue
		}
		if err := fs.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fmt.Errorf("write managed package files %s: create %s: %w", dir, filepath.Dir(dst), err)
		}
		if err := o.rename(fs, src, dst); err != nil {
			return fmt.Errorf("write managed package files %s: swap %s into place: %w", dir, relPath, err)
		}
	}
	return nil
}

// liveFileMatches reports whether dst is already a regular file with the same
// mode and bytes as the rendered src. Both modes are read back from the same
// filesystem rather than compared against the requested PackageFile.Mode, so
// a platform that cannot represent the requested bits (Windows) still compares
// like with like. Any error answers false: the caller then replaces, which is
// always correct.
func liveFileMatches(fs afero.Fs, src, dst string) bool {
	var live os.FileInfo
	var err error
	if l, ok := fs.(afero.Lstater); ok {
		live, _, err = l.LstatIfPossible(dst)
	} else {
		live, err = fs.Stat(dst)
	}
	if err != nil || !live.Mode().IsRegular() {
		return false
	}
	rendered, err := fs.Stat(src)
	if err != nil || rendered.Mode() != live.Mode() || rendered.Size() != live.Size() {
		return false
	}
	want, err := afero.ReadFile(fs, src)
	if err != nil {
		return false
	}
	have, err := afero.ReadFile(fs, dst)
	return err == nil && bytes.Equal(want, have)
}

// WriteManagedPackageFiles is the manifest-scoped TREE writer shared by every
// per-agent package writer (a command-file writer with exactly one rendered
// file per item, and a skill-package writer with SKILL.md plus its sibling
// files). dir is shared territory with user-authored files, and can ALSO be
// shared with a co-located surface's own managed set (see
// internal/shared/ledger's package doc), so it is never wiped wholesale: ctxloom tracks every file it wrote in
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
// dir and, once fully materialized there, moved into place file-by-file by
// rename, leaving any live file that is already identical untouched (see
// swapIntoPlace for what a concurrent reader can and cannot observe). Only after every new file is safely live are the
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
// render() call itself returns an error is treated the SAME way, a per-item
// warn-and-skip: one bad item (e.g. a command with no content) must not take
// the rest of a delivery down with it. What must hold instead is that this
// surface's previously-tracked files are never deleted before this validation
// runs. Under render-then-swap nothing is deleted until the new content has been
// confirmed live, so a single item's tolerated failure costs it that one
// item's slot in the ledger, never anyone else's, and never anything before
// the swap has actually landed. The one failure shape that DOES abort the
// whole call is every enabled item failing at once while content used to
// exist here — the empty-render guard below.
//
// dir itself is only created when at least one file is written; the manifest
// is (re)written only when at least one file was written. When cleanup leaves
// a now-empty subdirectory behind (a package's scripts/assets dir with every
// file removed), it is pruned bottom-up on a best-effort basis so a disabled
// skill leaves no debris.
//
// THE WHOLE CYCLE RUNS UNDER A LOCK KEYED ON dir (paths.HomePathFor(dir)), from
// the read of this surface's previous set to the ledger write that replaces
// it. Without it, a second writer of the same dir (another session, the MCP
// server, a hook) can complete a delivery between this call's read and its
// record; this call then records a set built from the stale read and the
// other writer's files stay on disk with no surface claiming them, beyond the
// reach of every later cleanup. The ledger's own marker lock does not cover
// this: it spans only the marker rewrite, not the read this cycle acts on.
// The lock is taken through files.Locks whatever files.Fs is: under the
// static writer files.Fs is a copy-on-write overlay of the controller's
// filesystem and files.Locks are the controller's own. There the cycle's
// writes reach disk only at the static writer's batch commit, after this lock
// is released, under that commit's per-file locks.
// See TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir.
func WriteManagedPackageFiles[T any](
	files safefs.Root,
	dir string,
	surface ledger.Surface,
	items []T,
	enabled func(T) bool,
	itemName func(T) string,
	render func(T) ([]PackageFile, error),
	opts ...ManagedWriteOption,
) error {
	lockPath, err := paths.HomePathFor(dir)
	if err != nil {
		return fmt.Errorf("agent: deriving home lock path for %s: %w", dir, err)
	}
	return safefs.WithLock(files.Locks, lockPath, func() error {
		return writeManagedPackageFilesLocked(files, dir, surface, items, enabled, itemName, render, opts...)
	})
}

// revertManagedSurface reverts one surface to empty: removes exactly the
// previously manifest-tracked set (never a co-located surface's entries, never
// a foreign file) and clears the ledger. This is the legitimate "nothing
// enabled" / "everything deduped against home" path — sharing the removal
// mechanics WriteManagedPackageFiles' phase 4 also uses, factored out so the
// empty and non-empty branches don't duplicate the ledger-removal walk.
func revertManagedSurface(rep report.Reporter, fs afero.Fs, dir string, surface ledger.Surface, previous []string, led ledger.Ledger) error {
	var removedDirs []string
	for _, name := range previous {
		path, ok := SafeCommandRelPath(dir, name)
		if !ok {
			rep.Warnf("skipping unsafe package ledger entry %q: not a relative path inside %s", name, dir)
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

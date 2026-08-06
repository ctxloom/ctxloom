package agent

import (
	"bytes"
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

// WriteManagedPackageFiles is the manifest-scoped TREE writer shared by every
// per-agent package writer (a command-file writer with exactly one rendered
// file per item, and a skill-package writer with SKILL.md plus its sibling
// files). dir is shared territory with user-authored files, so it is never
// wiped wholesale: ctxloom tracks every file it wrote in a manifest
// (the shared managed-content ledger, scoped to this surface) and removes
// exactly that set before writing the current
// one, so the written set always mirrors the enabled items.
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
// An item's files are validated (path-safety) as a whole BEFORE any of them is
// written, so a single unsafe path in a multi-file package skips the WHOLE
// item rather than leaving a partial tree on disk — the silent-no-op /
// partial-materialize discipline this codebase holds writers to. A write
// (I/O) failure partway through an item's files is rarer and warn-and-continues
// per file, keeping whatever succeeded (matching the historical single-file
// writer's fault tolerance).
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

	// Remove the previous ctxloom-written set for THIS SURFACE only. A
	// co-located surface's entries (antigravity and kiro write commands and
	// skills into one directory) must survive untouched — that separation used
	// to come from two manifest FILENAMES and now comes from the surface.
	//
	// Ledger entries are data, not trusted paths: a doctored line ("../x",
	// absolute) must not delete outside the managed tree.
	var removedDirs []string
	previous, err := led.Read(surface)
	if err != nil {
		return err
	}
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

	var written []string
	for _, item := range items {
		if !enabled(item) {
			continue
		}
		name := itemName(item)
		// Reject absolute/traversal names outright before any path is derived
		// from them. Nested names without traversal ("group/cmd") remain
		// allowed; how they map to paths is the renderer's choice.
		if _, ok := SafeCommandRelPath(dir, name); !ok {
			Warn("skipping package %q: name is not a relative path inside %s", name, dir)
			continue
		}
		files, err := render(item)
		if err != nil {
			Warn("skipping package %q: render failed: %v", name, err)
			continue
		}
		// Pre-validate every file's path before writing any of them: a single
		// unsafe path must not leave a partial package on disk.
		paths := make([]string, len(files))
		safe := true
		for i, f := range files {
			p, ok := SafeCommandRelPath(dir, f.RelPath)
			if !ok {
				Warn("skipping package %q: rendered path %q is not a relative path inside %s", name, f.RelPath, dir)
				safe = false
				break
			}
			paths[i] = p
		}
		if !safe {
			continue
		}

		for i, f := range files {
			path := paths[i]
			// Cross-scope dedup ("home/global wins"), per file: when writing into
			// a NON-home dir, skip a file byte-identical to the same-named one
			// already in the global dir. See WriteManagedCommandFiles for the
			// full rationale — the semantics are unchanged, just applied per file
			// of a (possibly multi-file) item instead of per item.
			if o.dedupHomeDir != "" && filepath.Clean(dir) != filepath.Clean(o.dedupHomeDir) {
				if homePath, ok := SafeCommandRelPath(o.dedupHomeDir, f.RelPath); ok {
					if existing, rerr := afero.ReadFile(fs, homePath); rerr == nil && bytes.Equal(existing, f.Content) {
						continue
					}
				}
			}
			if len(written) == 0 {
				if err := fs.MkdirAll(dir, 0755); err != nil {
					Warn("skipping package %q: create package dir %s: %v", name, dir, err)
					break
				}
			}
			if parent := filepath.Dir(path); parent != filepath.Clean(dir) {
				if err := fs.MkdirAll(parent, 0755); err != nil {
					Warn("skipping package %q: create package subdir %s: %v", name, parent, err)
					continue
				}
			}
			mode := f.Mode
			if mode == 0 {
				mode = defaultPackageFileMode
			}
			if err := afero.WriteFile(fs, path, f.Content, mode); err != nil {
				Warn("skipping package %q: write %s failed: %v", name, f.RelPath, err)
				continue
			}
			// afero.WriteFile only applies mode at file CREATION (via OpenFile's
			// perm); an existing file from a prior materialize keeps its old mode
			// unless explicitly chmod'd, which would silently let the exec bit
			// drift out of sync with a re-materialized script. Re-assert it, and
			// warn rather than ignore a failure of the very re-assert this
			// comment argues is needed for correctness.
			if err := fs.Chmod(path, mode); err != nil {
				Warn("package %q: chmod %s to %s failed: %v", name, f.RelPath, mode, err)
			}
			written = append(written, f.RelPath)
		}
	}

	// Written or not, the ledger is rewritten: an empty set must CLEAR this
	// surface's claim rather than leave the previous one standing, or the next
	// call would try to delete files this one already removed and, worse,
	// report ownership of content that no longer exists.
	return led.Write(surface, written)
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

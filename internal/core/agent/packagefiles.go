package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// This file is the managed TREE (package) writer: a command is the degenerate
// case of a package with exactly one file. WriteManagedCommandFiles
// (commandfiles.go) and WriteManagedSkillPackages (managed_skill_packages.go)
// are thin adapters over WriteManagedPackageFiles.

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
	items []T,
	enabled func(T) bool,
	itemName func(T) string,
	render func(T) ([]PackageFile, error),
	opts ...ManagedWriteOption,
) ([]string, error) {
	o := &managedWriteOptions{}
	for _, opt := range opts {
		opt(o)
	}
	fs := files.Fs

	// PHASE 1 — render + validate every enabled item OFF the live tree. Not one
	// byte under dir is touched in this phase, whatever happens: a render()
	// failure for one item is a per-item WARN-AND-SKIP, same as an unsafe
	// path, and NOT a whole-call abort — a caller-level content-validation
	// failure (e.g. a command with no body to render) is an expected,
	// recoverable per-item condition, tolerated so one bad item doesn't take
	// the rest of a delivery down with it.
	var prepared []preparedItem
	for _, item := range items {
		if !enabled(item) {
			continue
		}
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
	if len(prepared) == 0 {
		return nil, nil
	}

	// PHASE 2 — render the complete new file set into a temp SIBLING of dir
	// (same parent, so phase 3's renames stay on one volume), using ordinary
	// writes. These files are invisible to any reader of dir until phase 3, so
	// a failure here (disk full mid-item, an I/O error) still leaves dir
	// completely untouched — the same guarantee a render() error gets in phase
	// 1, extended to the write itself. dir's parent must exist before a
	// sibling of dir can be created in it; dir itself is created only when a
	// file lands in it (swapIntoPlace).
	if err := fs.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return nil, fmt.Errorf("write managed package files %s: create parent dir: %w", dir, err)
	}
	tempDir, err := afero.TempDir(fs, filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return nil, fmt.Errorf("write managed package files %s: create temp render tree: %w", dir, err)
	}
	defer func() { _ = fs.RemoveAll(tempDir) }()

	var written []string
	for _, p := range prepared {
		for _, f := range p.files {
			tempPath := filepath.Join(tempDir, f.RelPath)
			if err := fs.MkdirAll(filepath.Dir(tempPath), 0755); err != nil {
				return nil, fmt.Errorf("write managed package files %s: package %q: create temp subdir: %w", dir, p.name, err)
			}
			mode := f.Mode
			if mode == 0 {
				mode = defaultPackageFileMode
			}
			if err := afero.WriteFile(fs, tempPath, f.Content, mode); err != nil {
				return nil, fmt.Errorf("write managed package files %s: package %q: render %s: %w", dir, p.name, f.RelPath, err)
			}
			// afero.WriteFile's mode argument only applies at file CREATION
			// (via OpenFile's perm); tempPath was just created fresh, so this
			// is normally redundant, but re-assert explicitly in case a
			// backend doesn't honor perm-on-create — a warn-only best effort.
			if err := fs.Chmod(tempPath, mode); err != nil {
				o.rep.Warnf("package %q: chmod %s to %s failed: %v", p.name, f.RelPath, mode, err)
			}
			written = append(written, f.RelPath)
		}
	}

	// PHASE 3 — swap; see swapIntoPlace.
	if err := swapIntoPlace(fs, dir, tempDir, written); err != nil {
		return nil, err
	}
	delivered := make([]string, len(written))
	for i, rel := range written {
		delivered[i] = filepath.Join(dir, rel)
	}
	return delivered, nil
}

// swapIntoPlace moves each rendered temp file into dir at its final path with
// ONE rename — a substitution, never an unlink-then-create.
//
// Every rendered file is moved, including one whose live copy is already
// identical: under the static writer (fsstatic) this tree is an overlay, and
// the guarantee that an unchanged redelivery replaces nothing (on Windows a
// replace is MoveFileEx(MOVEFILE_REPLACE_EXISTING), a window in which a
// reader can find the file missing) belongs to the layer that writes to
// disk: safefs.Batch lands no file whose bytes are unchanged.
//
// Files are swapped one by one, so between the first rename and the last a
// reader can see a MIX of old and new versions across different files of one
// call. dir is never swapped wholesale: hand-authored files can sit beside
// managed ones, so a directory-level swap would evict them. A rename failure
// stops the loop: what already swapped stays new, the rest stays old.
func swapIntoPlace(fs afero.Fs, dir, tempDir string, written []string) error {
	for _, relPath := range written {
		dst := filepath.Join(dir, relPath)
		if err := fs.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fmt.Errorf("write managed package files %s: create %s: %w", dir, filepath.Dir(dst), err)
		}
		if err := safefs.Rename(fs, filepath.Join(tempDir, relPath), dst); err != nil {
			return fmt.Errorf("write managed package files %s: swap %s into place: %w", dir, relPath, err)
		}
	}
	return nil
}

// WriteManagedPackageFiles is the TREE writer shared by every per-agent
// package writer (a command-file writer with exactly one rendered file per
// item, and a skill-package writer with SKILL.md plus its sibling files). It
// renders every enabled item into dir and returns the host path of every file
// it placed there: the caller DECLARES exactly those (present.Delivered's
// Files) and the static writer owns them. It removes nothing. A file it
// placed on an earlier delivery and does not place now is undeclared, so the
// static writer's release of that earlier claim is what removes it; dir is
// shared territory with user-authored files, which no claim names.
//
// items is the caller's list of exportable things (CommandExport, SkillExport,
// …); enabled and itemName pick generic accessors off each item (kept as
// closures, not an interface, so neither export type needs a method just for
// this); render maps ONE enabled item to every file it materializes — a single
// entry for a command, SKILL.md plus every sibling file for a skill package.
// Every rendered path is validated with SafeCommandRelPath (command and skill
// names originate in bundle content, potentially remote): a path that
// escapes dir is rejected with a warning, never followed.
//
// RENDER-THEN-SWAP. Every enabled item is rendered and path-validated
// ENTIRELY OFF the live tree first (nothing under dir is touched); the
// complete new file set is then written into a temp sibling of dir and, once
// fully materialized there, moved into place file-by-file by rename (see
// swapIntoPlace for what a concurrent reader can and cannot observe). An
// item's files are validated as a whole BEFORE any of them is written, so a
// single unsafe path in a multi-file package skips the WHOLE item rather than
// leaving a partial tree on disk; an item whose render() returns an error is
// skipped the same way, with a warning, so one bad item does not take the
// rest of a delivery down with it.
//
// dir itself is only created when at least one file is written.
//
// The cycle runs under a lock keyed on dir (paths.HomePathFor(dir)), taken
// through files.Locks whatever files.Fs is: under the static writer files.Fs
// is a copy-on-write overlay of the controller's filesystem, the cycle's
// writes reach disk only at the static writer's batch commit, and files.Locks
// are a scope over the controller's own that keeps this lock held until that
// commit has landed (fsstatic's lockScope, where the order the locks are
// taken in is set down).
// See TestWriteManagedPackageFiles_ExcludesAConcurrentWriterOfItsDir.
func WriteManagedPackageFiles[T any](
	files safefs.Root,
	dir string,
	items []T,
	enabled func(T) bool,
	itemName func(T) string,
	render func(T) ([]PackageFile, error),
	opts ...ManagedWriteOption,
) (delivered []string, err error) {
	lockPath, err := paths.HomePathFor(dir)
	if err != nil {
		return nil, fmt.Errorf("agent: deriving home lock path for %s: %w", dir, err)
	}
	err = safefs.WithLock(files.Locks, lockPath, func() error {
		delivered, err = writeManagedPackageFilesLocked(files, dir, items, enabled, itemName, render, opts...)
		return err
	})
	return delivered, err
}

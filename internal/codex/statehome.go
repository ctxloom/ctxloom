package codex

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// StateHome is the project-scoped "virtual project dir" ctxloom points codex's
// home at for workDir — <workDir>/.ctxloom/state/engines/codex, from which
// cellScopedCodexHome derives the actual CODEX_HOME (<...>/codex/.codex).
//
// It is the SINGLE owner of that location, and that single ownership is the
// point: before it existed the run path resolved codex's home one way
// (resolveCodexProjectDir) and every static writer another (a bare
// <workDir>/.codex join), so `ctxloom profile materialize --backend codex` and
// a real codex run wrote to different roots — a delivery that looked complete
// from either vantage point and was complete from neither. Everything
// project-scoped now routes through here: the run path's in-tree arm, the
// CodexHookWriter, MCPRegistrar.ConfigPath, ProjectHome (the hook-scope
// collision check) and doctor's MCP surface list — pinned by
// TestCodexHome_RunPathAndStaticWritersAgree.
//
// WHY NOT <workDir>/.codex, where this used to be: CODEX_HOME is not a surface
// codex reads from the cwd, it is a home ctxloom RELOCATES — so ctxloom owns
// where it lands, and a relocated home holding seeded credentials belongs in
// the state tier (gitignored, unrebuildable — paths.EngineStateHome) rather
// than loose in the project root, where it needed its own hand-maintained
// ignore rules and sat next to the engine's genuinely cwd-keyed surfaces as if
// it were one of them. codex's cwd-keyed surface, AGENTS.md, is untouched by
// this and stays at the project root where codex natively reads it.
func StateHome(workDir string) string {
	return paths.EngineStateHome(filepath.Join(workDir, paths.AppDirName), "codex")
}

// legacyProjectHome is the PRE-RELOCATION in-tree codex home, <workDir>/.codex.
// Read-only and one-way: nothing delivers here any more, and migrateLegacyHome
// is the only thing that still looks at it.
func legacyProjectHome(workDir string) string {
	return filepath.Join(workDir, ConfigDirName)
}

// resolveInTreeHome returns StateHome(workDir), having first migrated a legacy
// <workDir>/.codex into it when that is safe (see migrateLegacyHome). It is the
// ONE function every in-tree entry point calls — the run path (Codex.Setup),
// the static settings writer (CodexHookWriter.WriteSettingsWithTrust) and the
// static surface delivery (configSurface.Deliver / the commands+skills
// closures) — so the migration cannot fire on one route and not the other.
//
// WRITE paths only. Status and RemoveSettings resolve through StateHome
// directly: a read must not move a user's files, and a config.toml left behind
// at the legacy path is inert once CODEX_HOME no longer points at it.
func resolveInTreeHome(fsys afero.Fs, workDir string) string {
	home := StateHome(workDir)
	migrateLegacyHome(fsys, workDir, home)
	return home
}

// migrateLegacyHome performs the ONE-WAY, ONE-TIME move of a pre-relocation
// <workDir>/.codex into <StateHome(workDir)>/.codex, preserving every byte the
// user has there: a hand-edited config.toml (foreign keys and all), prompts/,
// skills/, auth.json, and codex's own accumulated sessions/.
//
// A MOVE rather than a fresh empty home plus a warning, decided by the human on
// 2026-08-11: the alternative silently drops a user's engine configuration,
// which is this project's signature failure mode. Four cases, all of them
// announced:
//
//   - no legacy dir — the ordinary case, nothing happens and nothing is said.
//   - legacy only — moved, with a stderr note naming BOTH paths so the user can
//     find their files at the new location.
//   - BOTH exist — nothing is moved and a loud warning names both paths and
//     which one codex now reads. Merging two config.tomls is not something this
//     function can do correctly, and picking one silently would discard the
//     other.
//   - legacy is a SYMLINK (or any non-directory) — skipped with a warning, never
//     followed. Following it would move whatever it points at, which in this
//     project's own checkout has been the real ~/.codex.
func migrateLegacyHome(fsys afero.Fs, workDir, home string) {
	fsys = agent.GetFS(fsys)
	legacy := legacyProjectHome(workDir)
	target := filepath.Join(home, ConfigDirName)

	info, err := lstatIfPossible(fsys, legacy)
	switch {
	case err != nil && errors.Is(err, fs.ErrNotExist):
		return
	case err != nil:
		clidiag.Warn("ctxloom", "cannot inspect the legacy codex home %s (%v); skipping the one-time move to %s — check it by hand", legacy, err, target)
		return
	case info.Mode()&os.ModeSymlink != 0:
		clidiag.Warn("ctxloom", "the legacy codex home %s is a SYMLINK; refusing to follow it — ctxloom now uses %s, so move the contents there yourself if that link points at config you want", legacy, target)
		return
	case !info.IsDir():
		clidiag.Warn("ctxloom", "the legacy codex home %s is not a directory; leaving it alone — ctxloom now uses %s", legacy, target)
		return
	}

	if exists, statErr := afero.DirExists(fsys, target); statErr != nil {
		clidiag.Warn("ctxloom", "cannot inspect the codex home %s (%v); skipping the one-time move from %s", target, statErr, legacy)
		return
	} else if exists {
		clidiag.Warn("ctxloom", "two codex homes exist: the legacy %s and the current %s. ctxloom reads and writes the CURRENT one — %s — and moved nothing; merge or delete the legacy directory yourself", legacy, target, target)
		return
	}

	if err := moveTree(fsys, legacy, target); err != nil {
		clidiag.Warn("ctxloom", "could not move the legacy codex home %s to %s (%v); nothing was deleted — move it by hand, or delete it if you do not need it", legacy, target, err)
		return
	}
	clidiag.Warn("ctxloom", "moved codex's project config home from %s to %s (one time, contents preserved); ctxloom now points CODEX_HOME there", legacy, target)
}

// lstatIfPossible stats path WITHOUT following a final symlink where the
// filesystem supports it (afero.OsFs does; MemMapFs has no symlinks to
// confuse). A plain Stat would report a symlinked legacy home as the directory
// it points at, and the move would then relocate somebody's real ~/.codex.
func lstatIfPossible(fsys afero.Fs, path string) (os.FileInfo, error) {
	if lstater, ok := fsys.(afero.Lstater); ok {
		info, _, err := lstater.LstatIfPossible(path)
		return info, err
	}
	return fsys.Stat(path)
}

// moveTree relocates src to dst, preferring a rename and falling back to a
// copy-then-remove when the two are on different filesystems (rename returns
// EXDEV, which no amount of retrying fixes). The fallback removes the source
// only after every byte has landed, so a failed copy leaves the original
// intact.
func moveTree(fsys afero.Fs, src, dst string) error {
	if err := fsys.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	if err := fsys.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(fsys, src, dst); err != nil {
		return err
	}
	if err := fsys.RemoveAll(src); err != nil {
		return fmt.Errorf("remove the emptied %s after copying it to %s: %w", src, dst, err)
	}
	return nil
}

// copyTree duplicates the src subtree at dst, preserving each file's mode —
// auth.json is a live credential written 0600, and a copy that widened it would
// hand the whole point of the state tier away.
func copyTree(fsys afero.Fs, src, dst string) error {
	return afero.Walk(fsys, src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return fsys.MkdirAll(out, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			// Sockets, devices and dangling links are codex's business, not
			// ours; skipping one is better than failing the whole migration
			// over a file no engine reads.
			clidiag.Warn("ctxloom", "skipping %s while moving codex's config home: not a regular file", path)
			return nil
		}
		data, readErr := afero.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		if mkErr := fsys.MkdirAll(filepath.Dir(out), 0o755); mkErr != nil {
			return mkErr
		}
		return afero.WriteFile(fsys, out, data, info.Mode().Perm())
	})
}

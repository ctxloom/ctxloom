package tasks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SessionConfig parameterizes OpenSession. All fields are explicit (no env
// reads) so the resolution + migration logic is fully testable.
type SessionConfig struct {
	// Harp is the active session's harp name (CTXLOOM_SESSION_HARP). When
	// empty there is no session context and OpenSession falls back to the
	// legacy project store.
	Harp string

	// ResumedFrom is the harp this session resumed from (CTXLOOM_RESUMED_FROM),
	// or "" for a fresh (non-resume) session.
	ResumedFrom string

	// RestoreTasks reports whether the resume elected to carry tasks forward
	// (the [t] box in the resume picker). Ignored when ResumedFrom == "".
	RestoreTasks bool

	// SessionsRoot is the directory holding per-harp session dirs
	// (~/.ctxloom/sessions). Required when Harp != "".
	SessionsRoot string

	// ProjectDir is the working directory; the legacy store lives at
	// <ProjectDir>/.ctxloom/tasks.md and is the migration source for a
	// fresh session.
	ProjectDir string
}

// HarpStorePath returns the active task-store path for a harp-named session:
// <sessionsRoot>/<harp>/tasks.md.
func HarpStorePath(sessionsRoot, harp string) string {
	return filepath.Join(sessionsRoot, harp, "tasks.md")
}

// legacyStorePath returns the pre-session, project-local store path.
func legacyStorePath(projectDir string) string {
	return filepath.Join(projectDir, ".ctxloom", "tasks.md")
}

// OpenSession resolves the active task store for the current session and, the
// first time a harp store is established, MOVES the source tasks into it.
//
// Source selection:
//   - resume with tasks  → the resumed-from harp's store
//   - fresh session       → the legacy project store
//   - resume without tasks → nothing (the new harp starts with an empty store)
//
// The move is an atomic rename (with a cross-device copy+remove fallback), so
// exactly one session ever owns a given set of tasks: a concurrent second
// mover finds the source already gone and is left with an empty store rather
// than clobbering the first. Migration is best-effort — a failure warns and
// leaves the session with an empty store, never blocking the caller.
//
// With Harp == "" (no active session) OpenSession returns the legacy project
// store and performs no migration. If Harp is set but SessionsRoot is empty,
// it degrades to the legacy store with a warning.
func OpenSession(cfg SessionConfig) (*Store, error) {
	if cfg.Harp == "" {
		return Open(cfg.ProjectDir)
	}
	if cfg.SessionsRoot == "" {
		fmt.Fprintf(os.Stderr, "ctxloom: warning: no sessions root for harp %q; using legacy task store\n", cfg.Harp)
		return Open(cfg.ProjectDir)
	}

	dest := HarpStorePath(cfg.SessionsRoot, cfg.Harp)

	// Migrate only when this harp's store does not exist yet. Once it does,
	// it is the source of truth and we never re-pull a source over it.
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if source := migrationSource(cfg); source != "" && source != dest {
			if err := moveStore(source, dest); err != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: task store migration failed: %v\n", err)
			}
		}
	}

	return &Store{path: dest}, nil
}

// migrationSource picks the path whose tasks should seed a freshly
// established harp store. Returns "" when the store should start empty.
func migrationSource(cfg SessionConfig) string {
	if cfg.ResumedFrom != "" {
		if cfg.RestoreTasks {
			return HarpStorePath(cfg.SessionsRoot, cfg.ResumedFrom)
		}
		return "" // resumed but declined task carry-over
	}
	return legacyStorePath(cfg.ProjectDir) // fresh session
}

// moveStore moves src to dst, creating dst's parent dir. It is a no-op when
// src does not exist — which is the normal outcome when another process has
// already consumed the source (the rename-race loser), or when there is
// nothing to migrate. Prefers an atomic rename; on any rename failure
// (e.g. a cross-device move) it falls back to copy-then-remove.
func moveStore(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Rename failed (cross-device, or src vanished under a concurrent mover).
	data, err := os.ReadFile(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // someone else moved it; their content stands
		}
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	return os.Remove(src)
}

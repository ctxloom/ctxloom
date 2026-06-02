package tasks

import (
	"fmt"
	"os"
	"path/filepath"
)

// HarpStorePath returns the legacy per-session task-store path:
// <sessionsRoot>/<harp>/tasks.md. Retained for reading pre-ADR-0025 stores
// during migration (and for the compactor's per-harp essence inputs).
func HarpStorePath(sessionsRoot, harp string) string {
	return filepath.Join(sessionsRoot, harp, "tasks.md")
}

// legacyStorePath returns the pre-session, project-local store path.
func legacyStorePath(projectDir string) string {
	return filepath.Join(projectDir, ".ctxloom", "tasks.md")
}

// Located is a task paired with the harp of the session that originated it.
// OriginHarp == "" denotes a task with no recorded origin (a legacy or
// hand-added task).
type Located struct {
	Task
	OriginHarp string
}

// MigrateLegacyIfNeeded performs the one-time import of pre-ADR-0025 markdown
// stores into the per-project append-only log. It is a no-op once the log
// exists, so it is safe to call on every store open (guard with a cheap stat
// before gathering harps).
//
// Sources, in dedup order (first occurrence of a harp id wins): each per-harp
// session store under sessionsRoot — passed most-recent-first so the latest
// status carries — then the legacy project store (origin unset). Reads are
// best-effort: an unreadable store warns and is skipped (CLAUDE.md), never
// failing the migration.
func MigrateLegacyIfNeeded(logPath, projectDir, sessionsRoot string, harps []string) error {
	if _, err := os.Stat(logPath); err == nil {
		return nil // log already exists; nothing to migrate
	}
	seen := make(map[string]bool)
	var collected []Task
	gather := func(ts []Task, origin string) {
		for _, t := range ts {
			if t.HarpID == "" || seen[t.HarpID] {
				continue
			}
			seen[t.HarpID] = true
			t.OriginSession = origin
			collected = append(collected, t)
		}
	}
	if sessionsRoot != "" {
		for _, h := range harps {
			st := OpenPath(HarpStorePath(sessionsRoot, h))
			ts, err := st.Snapshot()
			if err != nil {
				fmt.Fprintf(os.Stderr, "ctxloom: warning: reading task store %s: %v\n", st.Path(), err)
				continue
			}
			gather(ts, h)
		}
	}
	if projectDir != "" {
		if ts, err := OpenPath(legacyStorePath(projectDir)).Snapshot(); err == nil {
			gather(ts, "")
		}
	}
	if len(collected) == 0 {
		return nil
	}
	log, err := OpenLog(logPath, "")
	if err != nil {
		return err
	}
	return log.Import(collected)
}

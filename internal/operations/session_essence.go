package operations

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// A session's distilled essence lives in one of two places and is resolved by
// ONE lookup order: the harp-dir layout (~/.ctxloom/sessions/<harp>/essence.md)
// first, then the legacy <appDir>/sessions/<sessionID>.md. SessionEssenceInfo
// answers "where is it / is there one" without opening the file, for listings
// that need that per row. Callers are `session show`, `session list`, `session
// query`, --full, and the memory MCP tools, so the order living in exactly one
// place is what stops a session reading as distilled in one command and
// pending in another.

// ReadHarpEssence returns the bytes of ~/.ctxloom/sessions/<harp>/essence.md.
// Errors when home can't be resolved or the file is missing.
func ReadHarpEssence(harpName string) ([]byte, error) {
	p, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// SessionEssenceInfo resolves a session's essence file path and whether it
// exists (i.e. the session is distilled), WITHOUT reading the file — so the
// listing can report essence_path/distilled cheaply for every row. It mirrors
// the harp-dir-first, legacy-second lookup order (the harp-dir layout needs no
// config; the legacy <sessionsDir>/<sessionID>.md needs appDir — pass "" to
// skip it).
//
// The existence check is inlined here rather than shared with the
// near-identical checks in internal/lm/isolation and internal/codex: those
// exclude only fs.ErrNotExist (a transcript path that is merely unreadable
// still counts as "there"), while this one also excludes directories
// (!info.IsDir()) — a deliberate difference in semantics, not an oversight,
// so it must not be unified with theirs.
func SessionEssenceInfo(harp string, entry *sessions.Entry, appDir string) (string, bool) {
	if p, err := paths.HarpEssencePath(harp); err == nil {
		if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
			return p, true
		}
	}
	if entry.SessionID != "" && appDir != "" {
		p := filepath.Join(paths.ProjectSessionsDir(appDir), entry.SessionID+".md")
		if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}

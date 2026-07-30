package cli

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A session's distilled essence lives in one of two places and is resolved by
// ONE lookup order: the harp-dir layout (~/.ctxloom/sessions/<harp>/essence.md)
// first, then the legacy <appDir>/sessions/<sessionID>.md. sessionEssenceInfo
// answers "where is it / is there one" without opening the file, for listings
// that need that per row; readSessionEssence is its reading face. Callers are
// `session show`, `session list`, `session query`, --full, and the memory MCP
// tools, so the order living in exactly one place is what stops a session
// reading as distilled in one command and pending in another.

// readHarpEssence returns the bytes of ~/.ctxloom/sessions/<harp>/essence.md.
// Errors when home can't be resolved or the file is missing.
func readHarpEssence(harpName string) ([]byte, error) {
	p, err := paths.HarpEssencePath(harpName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// readSessionEssence returns a session's distilled essence and whether one was
// found. It is the READING face of sessionEssenceInfo — one resolution order
// for both, so a session can never read as distilled in one command and
// pending in another. A pending session (no bound id) or a missing essence
// yields ("", false) rather than an error, so callers can present "not
// distilled yet" uniformly; an essence that exists but cannot be READ is a
// different fact and is reported rather than passed off as never-distilled.
func readSessionEssence(harp string, entry *sessions.Entry) (string, bool) {
	appDir := ""
	if cfg, err := config.Load(); err == nil {
		appDir = cfg.GetAppDir()
	}
	path, distilled := sessionEssenceInfo(harp, entry, appDir)
	if !distilled {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		clidiag.Warn("ctxloom", "essence for %s exists at %s but could not be read: %v", harp, path, err)
		return "", false
	}
	return string(data), true
}

// sessionEssenceInfo resolves a session's essence file path and whether it
// exists (i.e. the session is distilled), WITHOUT reading the file — so the
// listing can report essence_path/distilled cheaply for every row. It mirrors
// readSessionEssence's lookup order: the harp-dir layout first
// (~/.ctxloom/sessions/<harp>/essence.md, which needs no config), then the
// legacy <sessionsDir>/<sessionID>.md (which needs appDir; pass "" to skip it).
func sessionEssenceInfo(harp string, entry *sessions.Entry, appDir string) (string, bool) {
	if p, err := paths.HarpEssencePath(harp); err == nil && fileExists(p) {
		return p, true
	}
	if entry.SessionID != "" && appDir != "" {
		p := filepath.Join(paths.ProjectSessionsDir(appDir), entry.SessionID+".md")
		if fileExists(p) {
			return p, true
		}
	}
	return "", false
}

// fileExists reports whether p is an existing regular file (not a directory).
func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

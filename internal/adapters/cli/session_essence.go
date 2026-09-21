package cli

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// A session's distilled essence lives in one of two places, BOTH under the harp
// dir, and is resolved by ONE lookup order: the harp's current essence.md
// first, then that session's own per-rotation segments/<sessionID>.md —
// operations.SessionEssenceInfo answers "where is it / is there one" without
// opening the file, for listings that need that per row; readSessionEssence
// is its reading face. Callers are `session show`, `session list`, `session
// query`, --full, and the memory MCP tools, so the order living in exactly one
// place is what stops a session reading as distilled in one command and
// pending in another.

// readSessionEssence returns a session's distilled essence and whether one was
// found. It is the READING face of operations.SessionEssenceInfo — one
// resolution order for both, so a session can never read as distilled in one
// command and pending in another. A pending session (no bound id) or a
// missing essence yields ("", false) rather than an error, so callers can
// present "not distilled yet" uniformly; an essence that exists but cannot be
// READ is a different fact and is reported rather than passed off as
// never-distilled.
func readSessionEssence(v operations.SessionView) (string, bool) {
	if !v.Distilled {
		return "", false
	}
	data, err := os.ReadFile(v.EssencePath)
	if err != nil {
		clidiag.Warn("ctxloom", "essence for %s exists at %s but could not be read: %v", v.Harp, v.EssencePath, err)
		return "", false
	}
	return string(data), true
}

package operations

import (
	"os"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
)

// A session's distilled essence lives in one of two places, BOTH under the
// harp dir, and is resolved by ONE lookup order: the harp's current
// ~/.ctxloom/sessions/<harp>/essence.md first, then that session's own
// per-rotation segments/<sessionID>.md. SessionEssenceInfo answers "where is it
// / is there one" without opening the file, for listings that need that per
// row. Callers are `session show`, `session list`, `session query`, --full, and
// the memory MCP tools, so the order living in exactly one place is what stops
// a session reading as distilled in one command and pending in another.

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
// saveDistilled's own write order: the harp's current essence first, then this
// rotation's own copy under segments/, which is what still answers for a
// session whose harp has since been distilled again.
//
// The existence check is inlined here rather than shared with the
// near-identical checks in internal/lm/isolation and internal/codex: those
// exclude only fs.ErrNotExist (a transcript path that is merely unreadable
// still counts as "there"), while this one also excludes directories
// (!info.IsDir()) — a deliberate difference in semantics, not an oversight,
// so it must not be unified with theirs.
func SessionEssenceInfo(harp string, entry *sessions.Entry) (string, bool) {
	if p, err := paths.HarpEssencePath(harp); err == nil {
		if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
			return p, true
		}
	}
	if harp != "" && entry != nil && entry.SessionID != "" {
		if p, err := paths.ResolveHarpSegmentEssencePath(harp, entry.SessionID); err == nil {
			if info, statErr := os.Stat(p); statErr == nil && !info.IsDir() {
				return p, true
			}
		}
	}
	return "", false
}

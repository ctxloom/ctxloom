package operations

import (
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// A session's compacted essence lives in one of two places, BOTH in the
// session's output dir, and is resolved by ONE lookup order: the current
// <output>/essence.md first, then that session's own per-rotation
// <output>/segments/<sessionID>.md. SessionEssenceInfo answers "where is it /
// is there one" without opening the file, for listings that need that per row.
// Callers are `session show`, `session list`, `session query`, --full, and the
// memory MCP tools, so the order living in exactly one place is what stops a
// session reading as compacted in one command and pending in another.

// essenceOutputDir is the output dir a session's essence is read from: the
// entry's recorded one when the caller holds the entry, else as this process
// reaches it (sessions.OutputDirIn — a container's mount, or the record).
func essenceOutputDir(harp string, entry *sessions.Entry) (string, error) {
	if entry != nil && entry.OutputDir != "" {
		return entry.OutputDir, nil
	}
	return sessions.OutputDirIn(harp, os.Getenv)
}

// ReadHarpEssence returns the bytes of harp's current essence.md. Errors when
// the session's output dir cannot be resolved or the file is missing.
func ReadHarpEssence(harpName string) ([]byte, error) {
	out, err := essenceOutputDir(harpName, nil)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(out, paths.EssenceFileName))
}

// SessionEssenceInfo resolves a session's essence file path and whether it
// exists (i.e. the session is compacted), WITHOUT reading the file — so the
// listing can report essence_path/compacted cheaply for every row. It mirrors
// saveCompacted's own write order: the current essence first, then this
// rotation's own copy under segments/, which is what still answers for a
// session whose harp has since been compacted again.
//
// The existence check is inlined here rather than shared with the
// near-identical check in internal/adapters/isolation: that one excludes only
// fs.ErrNotExist (a transcript path that is merely unreadable still counts as
// "there"), while this one also excludes directories (!info.IsDir()) — a
// deliberate difference in semantics, not an oversight, so it must not be
// unified with that one.
func SessionEssenceInfo(harp string, entry *sessions.Entry) (string, bool) {
	out, err := essenceOutputDir(harp, entry)
	if err != nil {
		return "", false
	}
	if p := filepath.Join(out, paths.EssenceFileName); isRegularFile(p) {
		return p, true
	}
	if entry != nil && entry.SessionID != "" {
		if p := paths.OutputSegmentEssencePath(out, entry.SessionID); isRegularFile(p) {
			return p, true
		}
	}
	return "", false
}

// isRegularFile reports whether p exists and is not a directory.
func isRegularFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// outputEssenceItems is the derived population in the session's output dir:
// its current essence and each rotation's segment essence. Rel is relative to
// the output dir. A session with no output dir, or none written yet, has none.
func outputEssenceItems(entry *sessions.Entry) []PurgeItem {
	if entry == nil || entry.OutputDir == "" {
		return nil
	}
	candidates := []string{filepath.Join(entry.OutputDir, paths.EssenceFileName)}
	if segs, err := filepath.Glob(filepath.Join(entry.OutputDir, paths.SegmentsDirName, "*.md")); err == nil {
		candidates = append(candidates, segs...)
	}
	var out []PurgeItem
	for _, p := range candidates {
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		rel, _ := filepath.Rel(entry.OutputDir, p)
		out = append(out, PurgeItem{Path: p, Rel: filepath.ToSlash(rel), Class: PurgeClassDerived, Bytes: info.Size()})
	}
	return out
}

package sessions

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/harp"
	"github.com/ctxloom/ctxloom/internal/shared/textutil"
)

// IsSessionDir is THE predicate for "this entry under the sessions root is a
// session": a real directory (never a symlink, in either direction — a
// sweep that followed one would act on whatever it points at), whose name
// is a usable harp, and which carries the sidecar. Every walker over the
// root — the listing here, the reaper, doctor — must answer the question
// through this one function, or they will disagree about what exists.
//
// The sidecar is the table's identity member (paths.IdentityMember), so the
// predicate and the tree cannot disagree about which file makes a session.
//
// A directory WITHOUT the sidecar is not a session. That is what
// `session remove` (purge, then Forget) leaves behind on purpose, and what an
// engine-created directory nobody ever recorded looks like; neither is
// invented into a listing.
func IsSessionDir(root string, e fs.DirEntry) bool {
	if e.Type()&fs.ModeSymlink != 0 || !e.IsDir() {
		return false
	}
	if harp.Validate(e.Name()) != nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(root, e.Name(), filepath.FromSlash(paths.IdentityMember().Rel())))
	return err == nil && info.Mode().IsRegular()
}

// FirstLineSummary trims s to its first non-empty line and caps it at 80
// bytes: the one-line form a session's title takes.
func FirstLineSummary(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) > 80 {
		s = textutil.TruncateBytes(s, 80)
	}
	return s
}

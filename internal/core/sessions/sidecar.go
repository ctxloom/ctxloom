package sessions

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

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
	info, err := os.Lstat(filepath.Join(root, e.Name(), paths.SessionSidecarFileName))
	return err == nil && info.Mode().IsRegular()
}

// essenceFrontmatter is the slice of essence.md's frontmatter a listing
// needs. The file is written by internal/adapters/memory (distilledMeta); only the
// summary is read back here.
type essenceFrontmatter struct {
	Summary string `yaml:"summary"`
}

// fillFromEssence derives Summary and Detail from the harp's essence.md:
// the frontmatter's summary line and the leading bullets of the body's Open
// Items section. A missing or malformed essence leaves both empty — a
// never-distilled session simply has no summary.
func fillFromEssence(e *Entry) {
	if e == nil || e.HarpName == "" {
		return
	}
	p, err := paths.HarpEssencePath(e.HarpName)
	if err != nil {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return
	}
	var fm essenceFrontmatter
	if err := yaml.Unmarshal([]byte(rest[:end+1]), &fm); err != nil {
		return
	}
	e.Summary = FirstLineSummary(fm.Summary)
	e.Detail = PickerDetail(rest[end+len("\n---\n"):])
}

// maxPickerDetailLines caps the Open Items shown under a session row. With the
// subject line that's up to 5 lines per session, enough to tell two sessions
// apart without the listing growing unwieldy.
const maxPickerDetailLines = 4

// PickerDetail extracts the leading bullets of the body's "### Open Items"
// section as the entry's detail lines — the "what's left to do". Each
// returned line is normalized to a single line capped at 80 bytes; the "- "
// bullet marker is preserved for readability. Returns nil when the body has
// no Open Items section.
func PickerDetail(body string) []string {
	lines := strings.Split(body, "\n")
	inOpen := false
	var detail []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			// A heading ends the Open Items section once we're inside it; before
			// that, look for the Open Items heading specifically.
			if inOpen {
				break
			}
			if strings.Contains(strings.ToLower(t), "open items") {
				inOpen = true
			}
			continue
		}
		if !inOpen || t == "" {
			continue
		}
		if !strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "*") {
			continue
		}
		detail = append(detail, FirstLineSummary(t))
		if len(detail) >= maxPickerDetailLines {
			break
		}
	}
	return detail
}

// FirstLineSummary trims s to its first non-empty line and caps it at 80
// bytes, matching the one-line summary spec.
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

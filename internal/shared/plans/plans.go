// Package plans lists and reads session plan documents
// (~/.ctxloom/sessions/<harp>/<name>.plan.md). It is shared by taskloom (which
// surfaces plans via `taskloom plan list/show`) and ctxloom, so the session-dir
// location and frontmatter parsing live in one place. Pure value DTOs cross the
// wire; no agent or vscode coupling.
package plans

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// Plan is one session plan document.
type Plan struct {
	// Path is the absolute path to the .plan.md file.
	Path string `json:"path"`
	// Name is the file's base name without the .plan.md extension.
	Name string `json:"name"`
	// Title is the frontmatter `title`, falling back to Name when absent.
	Title string `json:"title"`
	// Session is the owning harp — the name of the directory holding the plan.
	Session string `json:"session"`
	// Sessions is the frontmatter `sessions:` list (every session that touched
	// the plan), as stamped by ctxloom's plan-stamp hook.
	Sessions []string `json:"sessions"`
	// ProjectDir is the project directory the owning session ran in, joined
	// from ~/.ctxloom/sessions/index.yaml. Empty when the plan could not be
	// attributed to any project (ephemeral/worktree session, pruned index
	// entry, hand-created plan file) — and also empty from the unscoped List /
	// ListHome, which do no attribution at all. Only the scoped listings
	// (ListHomeScoped, AttributeAll) populate it, so the field is additive:
	// existing consumers of the JSON shape see one new optional key.
	ProjectDir string `json:"project_dir,omitempty"`
}

// ListHome lists all session plans under ~/.ctxloom/sessions.
func ListHome() ([]Plan, error) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return nil, err
	}
	return List(root)
}

// List enumerates <root>/<harp>/*.plan.md, parsing each plan's frontmatter for a
// title and the sessions list. A missing root yields an empty list (no plans
// yet), not an error. Results are sorted by session then name for stable output.
//
// A session directory or plan file that cannot be READ is an error, not a
// shorter list. "I could not read it" must never render as "it is not there":
// a swallowed per-harp read error made an unreadable sessions tree print
// `(no plans)` and exit 0, indistinguishable from having no plans at all. The
// one case that is legitimately empty rather than failed is an entry that has
// VANISHED between listing and reading (a session reaped mid-scan) — that is
// skipped silently, because it genuinely holds no plans any more.
func List(root string) ([]Plan, error) {
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return []Plan{}, nil
		}
		return nil, err
	}
	out := []Plan{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				// Vanished between listing and reading (a session reaped
				// mid-scan): genuinely holds nothing any more, not a failure.
				return nil
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		segs := strings.Split(rel, string(filepath.Separator))
		harp := segs[0]
		if d.IsDir() {
			return nil // descend; plans may live in nested subdirectories (U115-F01/U132-F01)
		}
		if len(segs) < 2 {
			return nil // a file directly under root, not inside any harp dir — never a plan
		}
		if !strings.HasSuffix(d.Name(), paths.PlanFileExt) {
			return nil
		}
		name := strings.TrimSuffix(strings.Join(segs[1:], "/"), paths.PlanFileExt)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read plan %s: %w", path, err)
		}
		title := name
		t, ss := ParseFrontmatter(string(data))
		if t != "" {
			title = t
		}
		out = append(out, Plan{
			Path:     path,
			Name:     name,
			Title:    title,
			Session:  harp,
			Sessions: ss,
		})
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Show returns a plan file's content. The path must end in .plan.md and resolve
// inside ~/.ctxloom/sessions, so a crafted path can't read arbitrary files.
func Show(path string) (string, error) {
	if !strings.HasSuffix(path, paths.PlanFileExt) {
		return "", fmt.Errorf("not a plan file: %s", path)
	}
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("plan path is outside the sessions directory: %s", path)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// ParseFrontmatter extracts the `title` scalar and the `sessions:` block-list
// from a plan's leading YAML frontmatter (the `---` … `---` block). It is
// tolerant and dependency-free — only those two fields are read; a document with
// no frontmatter yields ("", nil).
func ParseFrontmatter(content string) (title string, sessions []string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", nil
	}
	inSessions := false
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "---" {
			break
		}
		if inSessions {
			item := strings.TrimSpace(line)
			if strings.HasPrefix(item, "- ") {
				// Unquoted for the same reason `title` is: a harp is the
				// value, not its YAML spelling. A quoted entry that kept its
				// quotes would never match the harp anywhere else, and the
				// writer round-trips scalar styles verbatim, so whatever
				// quoting a plan's author used survives into this parser.
				sessions = append(sessions, unquote(strings.TrimSpace(item[2:])))
				continue
			}
			// A non-item line that isn't indented ends the sessions block.
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inSessions = false
			}
		}
		switch {
		case strings.HasPrefix(line, "title:"):
			title = unquote(strings.TrimSpace(line[len("title:"):]))
		case strings.HasPrefix(line, "sessions:"):
			// YAML writes a sequence two ways and the paired writer emits
			// both: it round-trips through yaml.Node so that an author's
			// FLOW list (`sessions: [a, b]`) keeps its style and is merely
			// extended in place. A parser that understood only the block form
			// reported no sessions at all for a file it had just stamped.
			rest := strings.TrimSpace(line[len("sessions:"):])
			if rest == "" {
				inSessions = true
				continue
			}
			sessions = append(sessions, parseFlowSequence(rest)...)
		}
	}
	return title, sessions
}

// parseFlowSequence reads a single-line YAML flow sequence — `[a, b, "c"]` —
// into its items. Anything that is not bracketed yields nothing: a plain
// scalar or an anchor is not a list of sessions, and inventing one entry from
// it would be worse than reporting none. Commas inside quotes do not separate
// items, so a quoted value survives intact.
func parseFlowSequence(s string) []string {
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	inner := s[1 : len(s)-1]

	var items []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == ',':
			items = append(items, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	items = append(items, cur.String())

	out := make([]string, 0, len(items))
	for _, item := range items {
		if v := unquote(strings.TrimSpace(item)); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// unquote strips a single pair of matching surrounding quotes, if present.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

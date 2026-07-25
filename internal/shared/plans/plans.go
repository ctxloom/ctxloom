// Package plans lists and reads session plan documents
// (~/.ctxloom/sessions/<harp>/<name>.plan.md). It is shared by taskloom (which
// surfaces plans via `taskloom plan list/show`) and ctxloom, so the session-dir
// location and frontmatter parsing live in one place. Pure value DTOs cross the
// wire; no agent or vscode coupling.
package plans

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

const (
	sessionsDirName = "sessions"
	planExt         = ".plan.md"
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

// HomeSessionsDir returns ~/.ctxloom/sessions — the home-rooted directory whose
// per-harp subdirectories hold session plan files.
func HomeSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, paths.AppDirName, sessionsDirName), nil
}

// ListHome lists all session plans under ~/.ctxloom/sessions.
func ListHome() ([]Plan, error) {
	root, err := HomeSessionsDir()
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
	dirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return []Plan{}, nil
		}
		return nil, err
	}
	out := []Plan{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		harp := d.Name()
		dir := filepath.Join(root, harp)
		files, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read session dir %s: %w", harp, err)
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), planExt) {
				continue
			}
			path := filepath.Join(dir, f.Name())
			name := strings.TrimSuffix(f.Name(), planExt)
			title := name
			var sessions []string
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("read plan %s: %w", path, err)
			}
			t, ss := ParseFrontmatter(string(data))
			if t != "" {
				title = t
			}
			sessions = ss
			out = append(out, Plan{
				Path:     path,
				Name:     name,
				Title:    title,
				Session:  harp,
				Sessions: sessions,
			})
		}
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
	if !strings.HasSuffix(path, planExt) {
		return "", fmt.Errorf("not a plan file: %s", path)
	}
	root, err := HomeSessionsDir()
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
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
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
				sessions = append(sessions, strings.TrimSpace(item[2:]))
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
		case strings.TrimSpace(line) == "sessions:":
			inSessions = true
		}
	}
	return title, sessions
}

// unquote strips a single pair of matching surrounding quotes, if present.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

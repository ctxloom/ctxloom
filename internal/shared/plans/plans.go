// Package plans lists and reads session plan documents
// (~/.ctxloom/sessions/<harp>/persist/<name>.plan.md). It is shared by taskloom (which
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
			return nil // descend; plans may live in nested subdirectories
		}
		if len(segs) < 2 {
			return nil // a file directly under root, not inside any harp dir — never a plan
		}
		if !strings.HasSuffix(d.Name(), paths.PlanFileExt) {
			return nil
		}
		// A plan's NAME is stable across the persist/ migration. The path is
		// <harp>/persist/<name>.plan.md now and was <harp>/<name>.plan.md
		// before, and letting that show through would rename every plan in
		// every listing the moment the sweep ran — "design" becoming
		// "persist/design" for no reason a reader could act on. Only that one
		// leading segment is dropped; any deeper nesting is a real
		// distinction the name keeps.
		rest := segs[1:]
		if len(rest) > 1 && rest[0] == paths.PersistDirName {
			rest = rest[1:]
		}
		name := strings.TrimSuffix(strings.Join(rest, "/"), paths.PlanFileExt)
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

// Show returns a plan file's content. The path must end in .plan.md, must
// resolve — after every symlink is followed — inside ~/.ctxloom/sessions, and
// must name a regular file.
//
// Containment is checked on the RESOLVED path, not the lexical one. A lexical
// check answers "does this string sit under the root", which a symlink placed
// in the sessions directory defeats trivially: `notes.plan.md -> /etc/shadow`
// passes every string test and then hands back the target. The regular-file
// check closes the other half — a FIFO named `x.plan.md` is a lexically
// perfect plan path on which os.ReadFile blocks until something opens the
// other end, turning `plan show` into a hang.
func Show(path string) (string, error) {
	if !strings.HasSuffix(path, paths.PlanFileExt) {
		return "", fmt.Errorf("not a plan file: %s", path)
	}
	real, err := resolveContainedPlanPath(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// resolveContainedPlanPath follows every symlink in path and returns the real
// path, or an error if that path is not a regular file inside the sessions
// root. It is the whole of Show's safety check, kept apart from the read so
// the two cannot drift.
func resolveContainedPlanPath(path string) (string, error) {
	root, err := paths.HomeSessionsDir()
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	// The root itself may sit behind a symlink (a symlinked home, /var on
	// macOS). Resolve it too, or every resolved path would fail containment.
	if resolved, rerr := filepath.EvalSymlinks(rootAbs); rerr == nil {
		rootAbs = resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(real, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("plan path is outside the sessions directory: %s", path)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	return real, nil
}

// ParseFrontmatter extracts the `title` scalar and the `sessions:` list from a
// plan's leading YAML frontmatter. It is tolerant and dependency-free — only
// those two fields are read; a document with no frontmatter yields ("", nil).
//
// BOTH delimiters are required. An opening `---` that is never closed is not
// frontmatter: it is a document whose author did something else, and scanning
// it to EOF makes any body line shaped like `title:` — a heading in a fenced
// YAML example, a quoted snippet — silently become the plan's title. The
// paired writer refuses such a document outright rather than guess where the
// block ends, so accepting it here would have the two halves of one format
// disagreeing about which files even have frontmatter.
func ParseFrontmatter(content string) (title string, sessions []string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", nil
	}
	closed := false
	inSessions := false
	for _, raw := range lines[1:] {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "---" {
			closed = true
			break
		}
		if inSessions {
			item, isItem, stillOpen := sessionsBlockLine(line)
			inSessions = stillOpen
			if isItem {
				sessions = append(sessions, item)
				continue
			}
		}
		switch {
		case strings.HasPrefix(line, "title:"):
			title = unquote(strings.TrimSpace(line[len("title:"):]))
		case strings.HasPrefix(line, "sessions:"):
			flow, blockFollows := sessionsKeyLine(line)
			sessions = append(sessions, flow...)
			inSessions = blockFollows
		}
	}
	if !closed {
		return "", nil
	}
	return title, sessions
}

// sessionsBlockLine interprets one line while a `sessions:` block is open. It
// reports the item that line contributes (already unquoted, for the same
// reason `title` is: a harp is the value, not its YAML spelling, and a quoted
// entry that kept its quotes would match no harp anywhere else), whether the
// line was an item at all, and whether the block is still open afterwards — a
// non-item line that is not indented has left the block.
func sessionsBlockLine(line string) (item string, isItem, stillOpen bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "- ") {
		return unquote(strings.TrimSpace(trimmed[2:])), true, true
	}
	indented := strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
	return "", false, indented
}

// sessionsKeyLine interprets the `sessions:` key itself. YAML writes a
// sequence two ways and the paired writer emits both: it round-trips through
// yaml.Node so an author's FLOW list (`sessions: [a, b]`) keeps its style and
// is merely extended in place. A bare key means a block list follows on the
// lines after it.
func sessionsKeyLine(line string) (flow []string, blockFollows bool) {
	rest := strings.TrimSpace(line[len("sessions:"):])
	if rest == "" {
		return nil, true
	}
	return parseFlowSequence(rest), false
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
	items := splitOutsideQuotes(s[1 : len(s)-1])
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

// splitOutsideQuotes splits on commas that are not inside a quoted run, so a
// value containing a comma survives as one item. Quotes are kept: unquoting is
// the caller's step, and stripping them here would lose the information that
// the comma was protected.
func splitOutsideQuotes(s string) []string {
	var items []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
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
	return append(items, cur.String())
}

// unquote strips a single pair of matching surrounding quotes, if present.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// SessionPlanPaths returns the absolute paths of ONE harp's plan documents,
// sorted by base name. It is the single definition of "where does a session's
// plans live" for the readers that collect a session's own plans — the agent
// server's plan service (lm/grpc.ReadPlanFiles) and the runner's artifact
// stamper (mcp.artifactStamper.planCandidates) — so a plan an agent was told
// to write can never be somewhere none of them look.
//
// TWO DIRECTORIES, IN PRECEDENCE ORDER:
//
//	<harp>/persist  — paths.HarpPlansDir, where mcp.sessionInstructions now
//	    tells every session to write. It is the only part of the harp dir a
//	    containerized run gets bind-mounted, so it is the only location a
//	    container-authored plan survives in.
//	<harp>          — the harp TOP LEVEL, where the instruction used to point
//	    and where hand-authored plans still land. Read, never written to. A
//	    top-level file whose base name a persist/ file already claimed is
//	    SKIPPED: persist/ is the durable copy, and a stale pre-migration twin
//	    must not shadow it.
//
// Neither directory is walked recursively, deliberately: <harp>/ephemeral
// holds scratch git worktrees of the user's project (isolation's
// findEphemeralWorktrees reaps them), and a recursive walk would pull every
// *.plan.md checked out inside one into the session's plan list.
//
// FAULTS ARE RETURNED, NOT SWALLOWED. A missing directory is genuinely "no
// plans here" and is silent; an unresolvable home or an unreadable directory
// is a problem, because a caller that folds an empty result into distilled
// output makes "this session authored no plans" and "every plan it authored
// is unreachable" the same observation, permanently.
func SessionPlanPaths(harp string) ([]string, []error) {
	if harp == "" {
		return nil, nil
	}
	planDir, err := paths.HarpPlansDir(harp)
	if err != nil {
		return nil, []error{fmt.Errorf("plans for session %s omitted, plan dir unresolved: %w", harp, err)}
	}
	harpDir, err := paths.HarpDir(harp)
	if err != nil {
		return nil, []error{fmt.Errorf("plans for session %s omitted, session dir unresolved: %w", harp, err)}
	}
	var out []string
	var problems []error
	seen := map[string]bool{}
	for _, dir := range []string{planDir, harpDir} {
		entries, rerr := os.ReadDir(dir)
		if rerr != nil {
			if !os.IsNotExist(rerr) {
				problems = append(problems, fmt.Errorf("plans for session %s: directory %s unreadable: %w", harp, dir, rerr))
			}
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, paths.PlanFileExt) || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Slice(out, func(i, j int) bool { return filepath.Base(out[i]) < filepath.Base(out[j]) })
	return out, problems
}

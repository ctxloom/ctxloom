// Package plans lists and reads session plan documents
// (~/.ctxloom/sessions/<harp>/persist/<name>.plan.md). It is shared by taskloom (which
// surfaces plans via `taskloom plan list/show`) and ctxloom, so the session-dir
// location and frontmatter parsing live in one place. Pure value DTOs cross the
// wire; no agent or vscode coupling.
package plans

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

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

// frontmatter is the only part of a plan's leading YAML this package reads.
// Unlisted keys are ignored rather than rejected: a plan's frontmatter is the
// author's, and `status:` or `owner:` sitting beside these two is not an error.
type frontmatter struct {
	Title    string   `yaml:"title"`
	Sessions []string `yaml:"sessions"`
}

// ParseFrontmatter extracts the `title` scalar and the `sessions:` list from a
// plan's leading YAML frontmatter. Only those two fields are read; a document
// with no frontmatter yields ("", nil).
//
// The block is handed to yaml.v3 — the same parser the paired WRITER
// (memory.StampPlanFile, which round-trips the document through yaml.Node)
// already uses. That is the whole point: one file format needs one definition
// of what it says. A hand-rolled line scanner stood here and quietly disagreed
// with the writer about three ordinary shapes the writer preserves character
// for character — `title: hardening # rev2` (a comment, not part of the
// value), a doubled quote inside a single-quoted scalar (which is one quote,
// not two), and a literal block scalar (whose value is the indented text, not
// the `|`). The reader must report what the file MEANS, not the source text.
//
// BOTH delimiters are required, and that check is made HERE rather than left
// to YAML, which has no notion of frontmatter and would happily parse to EOF.
// An opening `---` that is never closed is not frontmatter: it is a document
// whose author did something else, and scanning it to EOF makes any body line
// shaped like `title:` — a heading in a fenced YAML example, a quoted snippet
// — silently become the plan's title. The paired writer refuses such a
// document outright rather than guess where the block ends, so accepting it
// here would have the two halves of one format disagreeing about which files
// even have frontmatter.
//
// Parsing is TOLERANT of a malformed field but never of a malformed document.
// A `sessions:` that is a scalar or a mapping cannot be a list of sessions, so
// none are reported — inventing one entry from it would be worse than
// reporting none — while a `title:` alongside it still comes back, because one
// unusable field is no reason to discard a good one. yaml.v3 reports exactly
// this case as a *yaml.TypeError after decoding everything it could. Any other
// error means the block is not a YAML document at all, and nothing is claimed
// about its contents.
func ParseFrontmatter(content string) (title string, sessions []string) {
	block, ok := frontmatterBlock(content)
	if !ok {
		return "", nil
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(block), &fm); err != nil {
		var typeErr *yaml.TypeError
		if !errors.As(err, &typeErr) {
			return "", nil
		}
	}
	if len(fm.Sessions) == 0 {
		// An absent key and an empty list are the same answer — no sessions —
		// and callers marshal this straight to JSON, where a nil slice is the
		// established shape for it.
		return fm.Title, nil
	}
	return fm.Title, fm.Sessions
}

// frontmatterBlock returns the text BETWEEN a document's opening and closing
// `---` fences, and whether the document had both. It is deliberately the only
// line-oriented step left: fences are a Markdown-frontmatter convention that
// the YAML parser knows nothing about, so something has to find them, and
// finding them wrong is what silently turns body prose into metadata.
//
// A line is a fence when it is `---` once surrounding whitespace is removed,
// and a trailing carriage return does not stop it being one, so a CRLF file
// delimits the same as an LF file. The block itself is returned with its line
// endings untouched: YAML accepts CRLF, and rewriting the author's bytes
// before parsing them would be one more place for the two halves of this
// format to drift apart.
//
// The newline that ends the block's LAST line belongs to the block, not to the
// closing fence, and is kept. It looks like punctuation and is not: a literal
// block scalar's value is chomped against exactly that byte, so dropping it
// turns a `title: |` of `wrapped` into "wrapped" where the file says
// "wrapped\n".
func frontmatterBlock(content string) (block string, ok bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", false
	}
	for i, raw := range lines[1:] {
		if strings.TrimSpace(strings.TrimRight(raw, "\r")) == "---" {
			if i == 0 {
				return "", true // an immediately closed block holds nothing
			}
			return strings.Join(lines[1:i+1], "\n") + "\n", true
		}
	}
	return "", false
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

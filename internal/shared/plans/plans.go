// Package plans lists and reads session plan documents: the <name>.plan.md
// files in each session's recorded output dir (sessions.Entry.OutputDir). It is shared by taskloom (which
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

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
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
	// from the owning session's record. Empty when the plan could not be
	// attributed to any project (ephemeral/worktree session, pruned index
	// entry, hand-created plan file) — and also empty from the unscoped List /
	// ListHome, which do no attribution at all. Only the scoped listings
	// (ListHomeScoped, AttributeAll) populate it, so the field is additive:
	// existing consumers of the JSON shape see one new optional key.
	ProjectDir string `json:"project_dir,omitempty"`
}

// ListHome lists every recorded session's plans.
func ListHome() ([]Plan, error) {
	m, err := sessions.Open(strictness.Sink("ctxloom"))
	if err != nil {
		return nil, err
	}
	all, err := m.ListAll()
	if err != nil {
		return nil, err
	}
	return ListSessions(all)
}

// ListSessions enumerates the *.plan.md files under each entry's output dir,
// parsing each plan's frontmatter for a title and the sessions list. An entry
// with no output dir, or whose output dir does not exist yet, holds no plans.
// Results are sorted by session then name for stable output.
//
// A directory or plan file that cannot be READ is an error, not a shorter
// list. "I could not read it" must never render as "it is not there". The one
// case that is legitimately empty rather than failed is an entry that has
// VANISHED between listing and reading — that is skipped silently, because it
// genuinely holds no plans any more.
func ListSessions(entries []sessions.Entry) ([]Plan, error) {
	out := []Plan{}
	for _, e := range entries {
		if e.OutputDir == "" {
			continue
		}
		found, err := listDir(e.OutputDir, e.HarpName)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Session != out[j].Session {
			return out[i].Session < out[j].Session
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// listDir is one session's plans: every *.plan.md under root, named by its
// path below root without the extension.
func listDir(root, harp string) ([]Plan, error) {
	var out []Plan
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read %s: %w", path, err)
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), paths.PlanFileExt) {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		name := strings.TrimSuffix(filepath.ToSlash(rel), paths.PlanFileExt)
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read plan %s: %w", path, err)
		}
		title := name
		t, ss := parseFrontmatterLabeled(string(data), path)
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
	return out, nil
}

// Show returns a plan file's content. The path must end in .plan.md, must
// resolve — after every symlink is followed — inside some recorded session's
// output dir, and must name a regular file.
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
// path, or an error if that path is not a regular file inside a recorded
// session's output dir. It is the whole of Show's safety check, kept apart
// from the read so the two cannot drift.
func resolveContainedPlanPath(path string) (string, error) {
	m, err := sessions.Open(strictness.Sink("ctxloom"))
	if err != nil {
		return "", err
	}
	all, err := m.ListAll()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !insideAnyOutputDir(real, all) {
		return "", fmt.Errorf("%w: %s", ErrPlanOutsideOutputDirs, path)
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

// ErrPlanOutsideOutputDirs is Show's refusal of a path that does not resolve
// inside any recorded session's output dir.
var ErrPlanOutsideOutputDirs = errors.New("plan path is outside every session's output dir")

// insideAnyOutputDir reports whether real lies under some entry's output dir,
// each resolved through its own symlinks (a symlinked home, /var on macOS) so
// a resolved path is compared with a resolved root.
func insideAnyOutputDir(real string, entries []sessions.Entry) bool {
	for _, e := range entries {
		if e.OutputDir == "" {
			continue
		}
		root, err := filepath.Abs(e.OutputDir)
		if err != nil {
			continue
		}
		if resolved, rerr := filepath.EvalSymlinks(root); rerr == nil {
			root = resolved
		}
		if strings.HasPrefix(real, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
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
// A REPEATED KEY is tolerated, resolved LAST-WINS, and announced. yaml.v3
// rejects a repeated key when decoding into a struct and discards the WHOLE
// mapping with it, which is why the block is walked as a yaml.Node first: a
// plan that says `title:` twice must not thereby lose its `sessions:` list to
// the reader while the paired writer keeps appending to it. The most recent
// value is the one the author most recently meant — the rule the hand-rolled
// scanner had — and a warning naming the key goes to stderr so the choice is
// made out loud rather than quietly. Stderr, not stdout: `taskloom plan list`
// prints a machine-readable listing on stdout that is parsed elsewhere.
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
	return parseFrontmatterLabeled(content, "")
}

// parseFrontmatterLabeled is ParseFrontmatter with the plan's path stamped on
// any duplicate-key warning. List walks every plan, and an unlabeled warning
// there names a key in a file the user cannot identify. Unexported so the
// label reaches the warning without changing ParseFrontmatter's exported
// signature; an empty path emits the unlabeled message.
func parseFrontmatterLabeled(content, path string) (title string, sessions []string) {
	block, ok := frontmatterBlock(content)
	if !ok {
		return "", nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(block), &doc); err != nil {
		return "", nil
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		// An empty block, or one whose root is a sequence or a bare scalar
		// rather than a mapping: it names nothing, so nothing is claimed.
		return "", nil
	}
	var fm frontmatter
	if err := resolveDuplicateKeys(doc.Content[0], path).Decode(&fm); err != nil {
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

// resolveDuplicateKeys returns a copy of a frontmatter mapping in which every
// key appears once, holding the LAST value the document gave it, and warns
// once per duplicated key, prefixed with path when one is given.
//
// It exists because the two available behaviours for a repeated key are not
// equally costly. yaml.v3's own answer — reject the mapping — throws away the
// keys that were NOT in dispute along with the one that was, so a plan that
// repeats `title:` reads back as having no sessions at all while
// memory.StampPlanFile goes on appending to the very list the reader now
// denies exists. Last-wins keeps the document's other keys and picks the
// author's most recent word on the disputed one; the warning is what stops
// that from being a silent guess.
//
// A non-scalar key (a sequence or mapping used as a key) is passed through
// untouched: it has no name to compare, and inventing one to dedup on would be
// guessing at a shape this format never has.
func resolveDuplicateKeys(mapping *yaml.Node, path string) *yaml.Node {
	out := *mapping
	out.Content = nil
	valueAt := make(map[string]int, len(mapping.Content)/2)
	warned := map[string]bool{}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		key, value := mapping.Content[i], mapping.Content[i+1]
		if key.Kind != yaml.ScalarNode {
			out.Content = append(out.Content, key, value)
			continue
		}
		if pos, seen := valueAt[key.Value]; seen {
			// Overwrite in place rather than append: the key keeps the
			// position it first held, so the mapping's order is the document's.
			out.Content[pos] = value
			if !warned[key.Value] {
				warned[key.Value] = true
				if path == "" {
					clidiag.Warn(diagProg(), "duplicate frontmatter key %q; using the last value", key.Value)
				} else {
					clidiag.Warn(diagProg(), "%s: duplicate frontmatter key %q; using the last value", path, key.Value)
				}
			}
			continue
		}
		out.Content = append(out.Content, key, value)
		valueAt[key.Value] = len(out.Content) - 1
	}
	return &out
}

// diagProg is the program name clidiag stamps on this package's warnings. It
// is read from the running executable rather than hardcoded, because this
// package is shared: the same duplicate-key warning is emitted under `taskloom
// plan list` and under ctxloom, and a line reading "ctxloom: warning:" from a
// taskloom invocation names a program the user did not run.
func diagProg() string {
	if prog := filepath.Base(os.Args[0]); prog != "" && prog != "." && prog != string(filepath.Separator) {
		return prog
	}
	return "ctxloom"
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
// sorted by base name: the *.plan.md files at the top of the session's
// recorded output dir. It is the single definition of "where does a
// session's plans live" for the readers that collect a session's own plans —
// e.g. the runner's artifact stamper — so a plan an agent was told to write
// can never be somewhere none of them look.
//
// The directory is not walked recursively: its subdirectories hold published
// reports and segment essences, which are not this session's plans.
//
// FAULTS ARE RETURNED, NOT SWALLOWED. A missing directory is genuinely "no
// plans here" and is silent; a session with no recorded output dir or an
// unreadable directory is a problem, because a caller that folds an empty
// result into distilled output makes "this session authored no plans" and
// "every plan it authored is unreachable" the same observation, permanently.
func SessionPlanPaths(harp string) ([]string, []error) {
	if harp == "" {
		return nil, nil
	}
	dir, err := sessions.OutputDir(harp)
	if err != nil {
		return nil, []error{fmt.Errorf("plans for session %s omitted, output dir unresolved: %w", harp, err)}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{fmt.Errorf("plans for session %s: directory %s unreadable: %w", harp, dir, err)}
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), paths.PlanFileExt) {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

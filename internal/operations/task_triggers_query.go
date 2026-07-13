package operations

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/triggers"
)

// Escalation query bounds. This is untrusted model output driving filesystem
// and git reads — every cap here exists to keep round 2 cheap and bounded no
// matter what a model (or a garbled response) asks for.
const (
	// maxQueriesPerTask caps how many queries one needs-investigation
	// verdict's escalation may carry (triggers.SanitizeQueries applies this).
	maxQueriesPerTask = 5
	// maxQueriesPerBatch caps total query EXECUTIONS across the whole
	// escalation round, regardless of how many tasks ask for them.
	maxQueriesPerBatch = 25
	// maxQueryOutputBytes caps one query's rendered result text.
	maxQueryOutputBytes = 2000
	// maxGrepMatches caps how many matching lines a grep query returns.
	maxGrepMatches = 20
	// maxGrepFilesScanned caps how many files a grep query walks before
	// giving up — a bound on WORK, independent of match count, so a
	// zero-match pattern over a huge tree still terminates promptly. Hitting
	// it is reported as a TRUNCATED (inconclusive) search, never as a clean
	// "no matches" — see queryGrep.
	maxGrepFilesScanned = 20000
	// maxGrepFileReadBytes caps how much of any single file grep reads.
	maxGrepFileReadBytes = 200 * 1024
	// gitLogPathScanCap bounds how many commits git_log_path scans (via the
	// existing git.Git.LogSince seam) looking for path matches.
	gitLogPathScanCap = 200
	// maxGitLogPathResults caps how many matching commits are reported.
	maxGitLogPathResults = 5
)

// executeQueries runs qs deterministically, one QueryResult per input query
// (always the same length as qs, so the caller can zip them back to their
// originating query 1:1), decrementing budget per execution. Once budget is
// exhausted, remaining queries get an error result rather than running — the
// batch-wide cap that keeps one chatty task from starving the rest.
func executeQueries(ctx context.Context, gitClient git.Git, repoDir string, otherByHarp map[string]tasks.Task, qs []triggers.Query, budget *int) []triggers.QueryResult {
	out := make([]triggers.QueryResult, 0, len(qs))
	for _, q := range qs {
		if *budget <= 0 {
			out = append(out, triggers.QueryResult{Query: q, Err: "batch query budget exhausted"})
			continue
		}
		*budget--
		out = append(out, executeQuery(ctx, gitClient, repoDir, otherByHarp, q))
	}
	return out
}

// executeQuery dispatches one whitelisted query to its executor.
// Re-validates q itself — defense in depth, so this function is safe to call
// directly (as the tests do) without relying on a caller having already run
// triggers.SanitizeQueries.
func executeQuery(ctx context.Context, gitClient git.Git, repoDir string, otherByHarp map[string]tasks.Task, q triggers.Query) triggers.QueryResult {
	if err := q.Validate(); err != nil {
		return triggers.QueryResult{Query: q, Err: err.Error()}
	}
	switch q.Type {
	case triggers.QueryPathExists:
		return queryPathExists(repoDir, q)
	case triggers.QueryGrep:
		return queryGrep(repoDir, q, defaultGrepBudget())
	case triggers.QueryGitLogPath:
		return queryGitLogPath(ctx, gitClient, repoDir, q)
	case triggers.QueryTaskStatus:
		return queryTaskStatus(otherByHarp, q)
	default:
		// Unreachable given Validate's whitelist check above.
		return triggers.QueryResult{Query: q, Err: fmt.Sprintf("unsupported query type %q", q.Type)}
	}
}

// safeRepoPath resolves rel (already syntax-checked by triggers.Query.
// Validate — no "..", not absolute) against repoDir and additionally
// resolves symlinks, rejecting a result that lands outside repoDir. The
// syntactic check alone cannot catch a symlink PLANTED inside the repo that
// points outside it; this is the second, filesystem-aware line of defense
// against a query escaping the repository.
func safeRepoPath(repoDir, rel string) (string, error) {
	if repoDir == "" {
		return "", fmt.Errorf("no repository configured")
	}
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRepo, rel)
	if !withinDir(absRepo, joined) {
		return "", fmt.Errorf("path %q escapes the repository", rel)
	}
	resolved, err := filepath.EvalSymlinks(joined)
	if err != nil {
		// The path (or a component of it) doesn't exist yet — not an escape,
		// just nothing to resolve. Callers that need existence (path_exists)
		// treat this as "does not exist"; grep simply finds nothing there.
		return joined, nil
	}
	resolvedAbs, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	if !withinDir(absRepo, resolvedAbs) {
		return "", fmt.Errorf("path %q escapes the repository via a symlink", rel)
	}
	return resolvedAbs, nil
}

// withinDir reports whether target is root itself or a descendant of it.
func withinDir(root, target string) bool {
	if target == root {
		return true
	}
	return strings.HasPrefix(target, root+string(filepath.Separator))
}

func queryPathExists(repoDir string, q triggers.Query) triggers.QueryResult {
	p, err := safeRepoPath(repoDir, q.Path)
	if err != nil {
		return triggers.QueryResult{Query: q, Err: err.Error()}
	}
	if _, statErr := os.Stat(p); statErr == nil {
		return triggers.QueryResult{Query: q, Output: "exists"}
	} else if os.IsNotExist(statErr) {
		return triggers.QueryResult{Query: q, Output: "does not exist"}
	} else {
		return triggers.QueryResult{Query: q, Err: statErr.Error()}
	}
}

// queryGrep is a bounded, read-only content search over the working tree,
// optionally narrowed by PathGlob. It uses Go's stdlib regexp — RE2-based, so
// an adversarial pattern is bounded to linear time rather than the
// catastrophic backtracking a PCRE-style engine risks — and stops after
// maxGrepFilesScanned files or maxGrepMatches lines, whichever comes first,
// regardless of tree size.
//
// PathGlob is a GLOB, not a directory (a model writes "**/*.go" into a field
// called path_glob, and it must mean what it says). A plain path with no glob
// metacharacters still works as a subtree scope. A scope that selects NOTHING
// on disk is an ERROR, never an empty result: an empty result reads to the
// model as "I searched and found nothing" — positive evidence of absence —
// and a query that never actually ran must never be able to say that.
// grepBudget bounds one grep query's work. Injected rather than read from the
// consts directly so a test can drive the truncation path without building a
// 20k-file fixture (no package-level vars; DI).
type grepBudget struct {
	maxFilesScanned  int
	maxMatches       int
	maxFileReadBytes int
}

func defaultGrepBudget() grepBudget {
	return grepBudget{
		maxFilesScanned:  maxGrepFilesScanned,
		maxMatches:       maxGrepMatches,
		maxFileReadBytes: maxGrepFileReadBytes,
	}
}

// skipGrepDir reports whether a directory is tool/vendor state rather than
// project source, and so must not be searched. This is not an optimization —
// it is load-bearing. A live run walked 208k files in this repo, 159k of them
// gitignored per-agent worktree COPIES under .claude/; the scan blew its
// budget on that junk long before reaching the actual source, and reported a
// truncated empty search. Hidden directories are agent/tool state (.git,
// .claude, .codex, .kiro), and node_modules/vendor are third-party trees; a
// trigger asking "does the CODEBASE contain X" means none of them.
func skipGrepDir(name string) bool {
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	return name == "node_modules" || name == "vendor"
}

func queryGrep(repoDir string, q triggers.Query, budget grepBudget) triggers.QueryResult {
	if repoDir == "" {
		return triggers.QueryResult{Query: q, Err: "no repository configured"}
	}
	re, err := regexp.Compile(q.Pattern)
	if err != nil {
		return triggers.QueryResult{Query: q, Err: fmt.Sprintf("invalid pattern: %v", err)}
	}
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return triggers.QueryResult{Query: q, Err: err.Error()}
	}

	// Scope resolution. Walking always starts at the repo root and filters by
	// the glob, so no path handed to the walker can originate outside it; a
	// metacharacter-free scope naming a real directory is narrowed to that
	// subtree purely as an optimization.
	// PathGlob's repo-containment (no absolute path, no "..", no NUL, no
	// backslash) was already enforced by q.Validate() at the top of
	// executeQuery; glob metacharacters pass those rules untouched, and are
	// harmless besides — matching runs against repo-relative paths produced by
	// a walk rooted INSIDE the repo, so a pattern can only ever select files
	// already within it. It cannot name a path the walk never produces.
	root := absRepo
	var scopeRe *regexp.Regexp
	if q.PathGlob != "" {
		if !hasGlobMeta(q.PathGlob) {
			p, perr := safeRepoPath(repoDir, q.PathGlob)
			if perr != nil {
				return triggers.QueryResult{Query: q, Err: perr.Error()}
			}
			if _, serr := os.Stat(p); serr != nil {
				return triggers.QueryResult{Query: q, Err: fmt.Sprintf("scope %q does not exist in the repository — nothing was searched", q.PathGlob)}
			}
			root = p
		} else {
			scopeRe, err = compileGlob(q.PathGlob)
			if err != nil {
				return triggers.QueryResult{Query: q, Err: fmt.Sprintf("invalid path_glob: %v", err)}
			}
		}
	}

	var matches []string
	scanned, considered := 0, 0
	truncated := false
	stop := fmt.Errorf("grep: bound reached")
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort: an unreadable entry is skipped, not fatal
		}
		if d.IsDir() {
			if skipGrepDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		// Only regular files. fs.WalkDir does not DESCEND into a symlink, but
		// os.ReadFile happily FOLLOWS one — so without this a symlink planted
		// in the repo would siphon outside-repo content straight into the
		// model's prompt.
		if !d.Type().IsRegular() {
			return nil
		}
		if scanned >= budget.maxFilesScanned || len(matches) >= budget.maxMatches {
			truncated = true
			return stop
		}
		rel, relErr := filepath.Rel(absRepo, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if scopeRe != nil && !scopeRe.MatchString(rel) {
			return nil
		}
		considered++
		scanned++
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if len(data) > budget.maxFileReadBytes {
			data = data[:budget.maxFileReadBytes]
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				if len(matches) >= budget.maxMatches {
					break
				}
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != stop {
		return triggers.QueryResult{Query: q, Err: walkErr.Error()}
	}
	// A glob that selected no file at all did not search anything, so it must
	// not report an empty (searched-and-found-nothing) result.
	if scopeRe != nil && considered == 0 {
		return triggers.QueryResult{Query: q, Err: fmt.Sprintf("path_glob %q matched no files in the repository — nothing was searched", q.PathGlob)}
	}

	// THE safety rule. A scan that hit its bound did not finish, so it cannot
	// speak to what it never looked at. Reporting "(no matches)" here would
	// hand the model positive evidence of absence it has not got — and a
	// confident false not-fired parks a live task forever. Zero matches from a
	// truncated scan is INCONCLUSIVE, and says so.
	if truncated && len(matches) == 0 {
		return triggers.QueryResult{Query: q, Err: fmt.Sprintf(
			"search was TRUNCATED after %d files without completing — this is INCONCLUSIVE, not evidence of absence. Narrow it with path_glob and try a more specific scope.", scanned)}
	}

	if len(matches) == 0 {
		return triggers.QueryResult{Query: q}
	}
	out := strings.Join(matches, "\n")
	if len(out) > maxQueryOutputBytes {
		out = out[:maxQueryOutputBytes] + "\n... (truncated)"
	}
	if truncated {
		out += "\n... (search truncated at the scan budget; MORE MATCHES MAY EXIST)"
	}
	return triggers.QueryResult{Query: q, Output: out}
}

// hasGlobMeta reports whether p carries any glob metacharacter, i.e. whether
// it must be matched as a pattern rather than resolved as a literal path.
func hasGlobMeta(p string) bool {
	return strings.ContainsAny(p, "*?[")
}

// compileGlob translates a repo-relative glob into an anchored regexp,
// supporting "**" (any number of path segments), "*" (any run of non-separator
// characters), and "?" (one non-separator character). Go's path.Match has no
// "**", and a model reaching for a recursive glob is the common case
// ("**/*.go"), so the translation is done here rather than rejecting it.
func compileGlob(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(glob); i++ {
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			// Match zero or more leading path segments, so "**/*.go" also
			// matches a .go file sitting at the repo root.
			sb.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(glob[i:], "**"):
			sb.WriteString(".*")
			i++
		case glob[i] == '*':
			sb.WriteString("[^/]*")
		case glob[i] == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(glob[i])))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

// queryGitLogPath answers "recent commits touching this path" by reusing the
// existing git.Git.LogSince seam (no new git interface method) and filtering
// its bounded commit window client-side to those whose changed files match
// the requested path exactly or fall under it as a directory prefix.
func queryGitLogPath(ctx context.Context, gitClient git.Git, repoDir string, q triggers.Query) triggers.QueryResult {
	if repoDir == "" || gitClient == nil {
		return triggers.QueryResult{Query: q, Err: "no repository configured"}
	}
	entries, err := gitClient.LogSince(ctx, repoDir, time.Time{}, gitLogPathScanCap)
	if err != nil {
		return triggers.QueryResult{Query: q, Err: err.Error()}
	}
	prefix := strings.TrimSuffix(q.Path, "/") + "/"
	var lines []string
	for _, e := range entries {
		touched := false
		for _, f := range e.Files {
			if f == q.Path || strings.HasPrefix(f, prefix) {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", shortLogSHA(e.SHA), e.Date.UTC().Format("2006-01-02"), e.Subject))
		if len(lines) >= maxGitLogPathResults {
			break
		}
	}
	if len(lines) == 0 {
		return triggers.QueryResult{Query: q, Output: "(no commits found touching this path)"}
	}
	return triggers.QueryResult{Query: q, Output: strings.Join(lines, "\n")}
}

// shortLogSHA trims a commit hash to a readable prefix, mirroring the pure
// package's own shortSHA (unexported there, so this is a small local twin
// rather than a cross-package export just for this).
func shortLogSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// queryTaskStatus answers from the ALREADY-FETCHED task list (built once per
// EvaluateTriggers call) rather than issuing a fresh store read per query —
// the store was already listed to build the evidence pack, so this is a
// map lookup, not I/O.
func queryTaskStatus(byHarp map[string]tasks.Task, q triggers.Query) triggers.QueryResult {
	t, ok := byHarp[q.HarpID]
	if !ok {
		return triggers.QueryResult{Query: q, Output: "no task found with that harp id"}
	}
	return triggers.QueryResult{Query: q, Output: fmt.Sprintf("status=%s text=%q", t.Status, t.Text)}
}

package triggers

import (
	"fmt"
	"path"
	"strings"
)

// QueryType is one of a CLOSED, WHITELISTED set of evidence requests a model
// may make when it returns needs-investigation on round 1 of trigger triage.
// The model picks from this set; it never writes code, a shell command, or an
// arbitrary path expression — see Query.Validate.
type QueryType string

const (
	// QueryPathExists asks whether a repo-relative path exists (file or dir).
	QueryPathExists QueryType = "path_exists"
	// QueryGrep asks for a bounded, read-only content search, optionally
	// scoped to a repo-relative glob.
	QueryGrep QueryType = "grep"
	// QueryGitLogPath asks for recent commits touching a repo-relative path.
	QueryGitLogPath QueryType = "git_log_path"
	// QueryTaskStatus asks for the current status of another task by harp id.
	QueryTaskStatus QueryType = "task_status"
)

// QueryTypes lists every whitelisted query type, in the order presented to
// the model.
func QueryTypes() []QueryType {
	return []QueryType{QueryPathExists, QueryGrep, QueryGitLogPath, QueryTaskStatus}
}

// Valid reports whether t is one of the four whitelisted query types.
func (t QueryType) Valid() bool {
	switch t {
	case QueryPathExists, QueryGrep, QueryGitLogPath, QueryTaskStatus:
		return true
	}
	return false
}

// Query is one evidence request a model may attach to a round-1
// needs-investigation verdict. It is untrusted model output: nothing in it is
// executed as-is — the ctxloom-side executor (internal/operations) only ever
// dispatches on Type and reads the named fields, and every path here MUST
// pass Validate before it touches the filesystem or git. This shape carries
// every query type's fields; a given Type only uses the subset it needs (see
// Validate for which).
type Query struct {
	Type QueryType `json:"type"`
	// Path is a repo-relative path (path_exists, git_log_path).
	Path string `json:"path,omitempty"`
	// Pattern is the search text (grep). It rides Go's RE2-based regexp
	// engine ctxloom-side, which is immune to catastrophic backtracking, so
	// an adversarial pattern can be slow to author but not pathological to
	// run.
	Pattern string `json:"pattern,omitempty"`
	// PathGlob optionally scopes a grep to a repo-relative subtree.
	PathGlob string `json:"path_glob,omitempty"`
	// HarpID names the other task to look up (task_status).
	HarpID string `json:"harp_id,omitempty"`
}

// Validate performs the SYNTACTIC whitelist and repo-containment checks: is
// Type one of the four whitelisted values, are the fields that type needs
// present, and — for anything path-shaped — is the path repo-relative with no
// traversal. It does NOT touch the filesystem (an on-disk symlink that
// resolves outside the repo can't be caught by string inspection alone); the
// ctxloom-side executor re-checks containment after resolving symlinks before
// any actual read. Validate is the first, cheap line of defense against
// adversarial or garbled model output — it never panics on any input.
func (q Query) Validate() error {
	if !q.Type.Valid() {
		return fmt.Errorf("triggers: query type %q is not in the whitelist", q.Type)
	}
	switch q.Type {
	case QueryPathExists, QueryGitLogPath:
		return validateRepoPath(q.Path)
	case QueryGrep:
		if strings.TrimSpace(q.Pattern) == "" {
			return fmt.Errorf("triggers: grep query has no pattern")
		}
		if q.PathGlob != "" {
			return validateRepoPath(q.PathGlob)
		}
		return nil
	case QueryTaskStatus:
		if strings.TrimSpace(q.HarpID) == "" {
			return fmt.Errorf("triggers: task_status query has no harp_id")
		}
		return nil
	default:
		// Unreachable given the Valid() guard above, but Validate must never
		// silently accept a type it doesn't recognize how to check.
		return fmt.Errorf("triggers: unhandled query type %q", q.Type)
	}
}

// validateRepoPath rejects, by syntax alone, anything that could steer a
// later filesystem read outside the repository: an empty/blank path, an
// absolute path (unix or windows-style), a path containing ".." after
// cleaning, a backslash (not a unix repo-relative path), or a NUL byte.
func validateRepoPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("triggers: query path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("triggers: query path %q contains a NUL byte", p)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("triggers: query path %q contains a backslash, not a repo-relative unix path", p)
	}
	if path.IsAbs(p) {
		return fmt.Errorf("triggers: query path %q must be repo-relative, not absolute", p)
	}
	if len(p) >= 2 && p[1] == ':' {
		// A drive-letter path (C:\...) already failed the backslash check
		// above in the normal case, but "C:/foo" would slip past it — reject
		// the drive-letter shape directly.
		return fmt.Errorf("triggers: query path %q looks like a windows absolute path", p)
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("triggers: query path %q escapes the repository", p)
	}
	return nil
}

// SanitizeQueries filters qs down to the queries that pass Validate,
// preserving order, capped at maxPerTask (<=0 means unbounded). A model that
// asks for one bad query among several good ones loses only the bad one, not
// the whole batch — malformed input degrades gracefully rather than losing
// legitimate follow-up evidence. Never panics on nil, empty, or garbled input.
func SanitizeQueries(qs []Query, maxPerTask int) []Query {
	var out []Query
	for _, q := range qs {
		if err := q.Validate(); err != nil {
			continue
		}
		out = append(out, q)
		if maxPerTask > 0 && len(out) >= maxPerTask {
			break
		}
	}
	return out
}

// QueryResult is the deterministic, ctxloom-gathered answer to one Query,
// carried into round 2's follow-up prompt. Output is empty and Err is set
// when the query failed or was rejected at execution time (e.g. a path that
// passed syntactic Validate but resolved outside the repo via a symlink) —
// the model still sees that its query didn't produce evidence, rather than
// the query silently vanishing.
type QueryResult struct {
	Query  Query  `json:"query"`
	Output string `json:"output,omitempty"`
	Err    string `json:"error,omitempty"`
}

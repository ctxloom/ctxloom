package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/gitutil"
)

// execGit is the default Git: it shells out to the system git binary. Stateless —
// every method receives its working directory, so one instance is safe to share
// across the concurrent fan-out.
type execGit struct{}

// NewExec returns the default git binary-backed implementation.
func NewExec() Git { return execGit{} }

// isRepoTimeout bounds the non-context IsRepo probe (it satisfies a bool-returning
// signature, so it self-limits rather than blocking indefinitely).
const isRepoTimeout = 5 * time.Second

// IsRepo reports whether dir is inside a working tree via rev-parse (which handles
// linked worktrees, where .git is a file). Any error → false (fault tolerant).
func (execGit) IsRepo(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), isRepoTimeout)
	defer cancel()
	out, err := output(ctx, dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// CommonDir returns the absolute common git dir. rev-parse yields it relative to
// dir for the main repo (".git") and absolute for a linked worktree, so resolve
// the relative form against dir.
func (execGit) CommonDir(ctx context.Context, dir string) (string, error) {
	out, err := output(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Clean(p), nil
}

// WorktreeAdd creates a detached worktree at path checked out to ref.
func (execGit) WorktreeAdd(ctx context.Context, repoDir, path, ref string) error {
	return run(ctx, repoDir, "worktree", "add", "--detach", path, ref)
}

// WorktreeRemove removes the worktree at path. Never --force: git refuses a
// dirty or locked worktree, which is the WIP-safe behavior every caller wants.
func (execGit) WorktreeRemove(ctx context.Context, repoDir, path string) error {
	return run(ctx, repoDir, "worktree", "remove", path)
}

// WorktreeList parses the repo-global porcelain worktree list.
func (execGit) WorktreeList(ctx context.Context, repoDir string) ([]Worktree, error) {
	out, err := output(ctx, repoDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

// WorktreePrune drops stale worktree admin files.
func (execGit) WorktreePrune(ctx context.Context, repoDir string) error {
	return run(ctx, repoDir, "worktree", "prune")
}

// mergedBranchesTimeout bounds MergedBranches — it runs once per foreign
// worktree on EVERY `ctxloom doctor` invocation (git.go's doc), so, like
// isRepoTimeout above, it self-limits rather than relying on every caller's
// context to already carry a deadline.
const mergedBranchesTimeout = 5 * time.Second

// MergedBranches lists local branches already merged into ref (git branch
// --merged). ref="" resolves to repoDir's own current branch first.
func (g execGit) MergedBranches(ctx context.Context, repoDir, ref string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, mergedBranchesTimeout)
	defer cancel()
	if ref == "" {
		cur, err := g.CurrentBranch(ctx, repoDir)
		if err != nil {
			return nil, err
		}
		ref = cur
	}
	out, err := output(ctx, repoDir, "branch", "--merged", ref, "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	var branches []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, nil
}

// UpdateIndexSkipWorktree toggles the skip-worktree bit on a tracked file.
func (execGit) UpdateIndexSkipWorktree(ctx context.Context, dir, file string, skip bool) error {
	flag := "--no-skip-worktree"
	if skip {
		flag = "--skip-worktree"
	}
	return run(ctx, dir, "update-index", flag, file)
}

// ListTracked returns the tracked files under dir matching pathspecs, parsed from
// NUL-separated `git ls-files -z` output (so paths with spaces/newlines survive).
// An empty pathspec list matches nothing (git would list every tracked file, which
// the callers never want), so it short-circuits to no results.
func (execGit) ListTracked(ctx context.Context, dir string, pathspecs ...string) ([]string, error) {
	if len(pathspecs) == 0 {
		return nil, nil
	}
	args := append([]string{"ls-files", "-z", "--"}, pathspecs...)
	out, err := output(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// IsDirty reports whether the working tree at dir has uncommitted OR untracked
// changes (porcelain output non-empty).
func (execGit) IsDirty(ctx context.Context, dir string) (bool, error) {
	out, err := output(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// HasIgnoredContent reports whether dir holds any ignored/excluded file —
// `status --porcelain --ignored=matching` lists each one with a leading
// "!! " marker (distinct from the "?? " IsDirty already treats as WIP), so
// this deliberately does NOT reuse IsDirty's plain --porcelain output: that
// call intentionally omits ignored entries.
func (execGit) HasIgnoredContent(ctx context.Context, dir string) (bool, error) {
	out, err := output(ctx, dir, "status", "--porcelain", "--ignored=matching")
	if err != nil {
		return false, err
	}
	for _, line := range splitNonEmptyLines(out) {
		if strings.HasPrefix(line, "!!") {
			return true, nil
		}
	}
	return false, nil
}

// Bounds for the repo-state evidence, so a large monorepo can never blow the
// evaluation prompt's budget.
const (
	defaultRepoDirsMax       = 400
	defaultWorkingChangesMax = 100
)

// RepoDirs inventories the directories that exist in the repo. It unions
// TRACKED paths (git ls-files) with UNTRACKED ones (git ls-files --others,
// honoring .gitignore): a directory holding only uncommitted work is still a
// directory that EXISTS, and it is precisely the case commit history cannot
// reveal — the bug that made an "does package X exist" trigger evaluate as
// not-fired while the package sat in the working tree.
//
// Every ANCESTOR of a listed file is inventoried, not just its immediate
// parent: a package whose content all lives in subdirectories exists just as
// much as one holding files directly, and an existence-style trigger asking
// about it must not read its absence as proof it was never created.
func (execGit) RepoDirs(ctx context.Context, dir string, maxDirs int) ([]string, error) {
	if maxDirs <= 0 {
		maxDirs = defaultRepoDirsMax
	}
	tracked, err := output(ctx, dir, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	untracked, err := output(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	for _, f := range strings.Split(tracked+"\x00"+untracked, "\x00") {
		if f == "" {
			continue
		}
		// Walk up to the repo root. Stopping at the first already-recorded
		// ancestor is safe: a directory only ever enters the set together
		// with all of its own ancestors.
		for d := parentDir(filepath.ToSlash(f)); d != ""; d = parentDir(d) {
			if _, ok := seen[d]; ok {
				break
			}
			seen[d] = struct{}{}
		}
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	if len(dirs) > maxDirs {
		dirs = dirs[:maxDirs]
	}
	return dirs, nil
}

// parentDir returns p's parent directory for a slash-separated, repo-relative
// git path, or "" once there is none left (a repo-root entry carries no
// directory to inventory). Deliberately not path.Dir: that returns "." for a
// root entry, which is not a directory this inventory reports.
func parentDir(p string) string {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// WorkingChanges returns the porcelain working-tree status, bounded.
//
// -uall (untracked-files=all) is load-bearing, not cosmetic: plain
// --porcelain COLLAPSES a wholly-untracked directory to a single "?? path/"
// entry and never names the files inside it. A brand-new package — precisely
// the "does X exist yet" case this evidence exists to answer — would then
// reach the model as a bare directory marker with its contents invisible.
func (execGit) WorkingChanges(ctx context.Context, dir string, maxEntries int) ([]string, error) {
	if maxEntries <= 0 {
		maxEntries = defaultWorkingChangesMax
	}
	out, err := output(ctx, dir, "status", "--porcelain", "-uall")
	if err != nil {
		return nil, err
	}
	var changes []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		changes = append(changes, line)
		if len(changes) >= maxEntries {
			break
		}
	}
	return changes, nil
}

// CurrentBranch returns the checked-out branch name, or git's own "HEAD"
// sentinel when detached.
func (execGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := output(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CommitAll stages every dirty change (git add -A) and commits with message,
// returning the new SHA and the files changed versus the pre-commit HEAD —
// see the Git interface doc for why the caller needs that second return
// rather than trusting a bare "commit succeeded".
func (g execGit) CommitAll(ctx context.Context, dir, message string) (string, []string, error) {
	preSHA, err := preCommitHead(ctx, dir)
	if err != nil {
		return "", nil, fmt.Errorf("resolving pre-commit HEAD: %w", err)
	}

	if err := run(ctx, dir, "add", "-A"); err != nil {
		return "", nil, fmt.Errorf("staging changes: %w", err)
	}
	if _, err := outputStdin(ctx, dir, message, "commit", "--file", "-"); err != nil {
		return "", nil, fmt.Errorf("committing: %w", err)
	}

	postOut, err := output(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, fmt.Errorf("resolving post-commit HEAD: %w", err)
	}
	postSHA := strings.TrimSpace(postOut)

	changed, err := commitContents(ctx, dir, preSHA, postSHA)
	if err != nil {
		// The commit itself landed (postSHA is real) — surface it even
		// though verification couldn't complete, rather than discarding a
		// real commit's SHA because the follow-up diff call failed.
		return postSHA, nil, fmt.Errorf("commit %s landed but verifying its content failed: %w", postSHA, err)
	}
	return postSHA, changed, nil
}

// preCommitHead resolves dir's current HEAD commit, or "" when HEAD is UNBORN
// — a repository with zero commits, where HEAD names a branch ref that does
// not exist yet. `git rev-parse HEAD` fails identically for "unborn" and "not
// a repository at all", and only the first is a state CommitAll may proceed
// from, so the two are told apart by symbolic-ref: it resolves only inside a
// repository whose HEAD points at a (possibly unborn) branch.
func preCommitHead(ctx context.Context, dir string) (string, error) {
	out, err := output(ctx, dir, "rev-parse", "HEAD")
	if err == nil {
		return strings.TrimSpace(out), nil
	}
	if _, serr := output(ctx, dir, "symbolic-ref", "--quiet", "HEAD"); serr != nil {
		return "", err
	}
	return "", nil
}

// commitContents returns the files postSHA carries versus preSHA. An empty
// preSHA means postSHA is a ROOT commit (see preCommitHead), which has no
// parent to diff against — --root makes git list the whole commit instead,
// rather than yielding the empty list CommitAll's caller treats as evidence
// the commit landed with no content.
func commitContents(ctx context.Context, dir, preSHA, postSHA string) ([]string, error) {
	if preSHA == "" {
		out, err := output(ctx, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", postSHA)
		if err != nil {
			return nil, err
		}
		return splitNonEmptyLines(out), nil
	}
	return diffNameOnly(ctx, dir, preSHA, postSHA)
}

// diffNameOnly returns the names of files that differ between two refs.
// Unexported: it is CommitAll's own verification step (it used to be on
// the public Git interface with no caller outside this package, forcing a
// Fake stub and two dead fields for no consumer).
func diffNameOnly(ctx context.Context, dir, a, b string) ([]string, error) {
	out, err := output(ctx, dir, "diff", "--name-only", a, b)
	if err != nil {
		return nil, err
	}
	return splitNonEmptyLines(out), nil
}

// Clone clones url into dir, optionally pinning the branch to check out. Run
// with an empty cwd: both url and dir are the command's own arguments, so
// there is no repository to be "in" — and, per the package doc, cmd.Dir is the
// only thing that selects one.
func (execGit) Clone(ctx context.Context, url, dir, branch string) error {
	args := []string{"clone", "--single-branch"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	// "--" separates options from operands: a URL or directory beginning with
	// "-" would otherwise be read as a flag.
	args = append(args, "--", url, dir)
	return run(ctx, "", args...)
}

// HeadSHA resolves dir's HEAD commit.
func (execGit) HeadSHA(ctx context.Context, dir string) (string, error) {
	out, err := output(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FileBlobSHA reads the blob SHA of path at ref from `git ls-tree`, whose
// empty-output-on-missing-path behaviour is what separates ABSENT from
// COULD-NOT-ASK (see the Git interface doc). Output shape:
//
//	100644 blob 0123abc…\tpath/to/file
func (execGit) FileBlobSHA(ctx context.Context, dir, ref, path string) (string, error) {
	out, err := output(ctx, dir, "ls-tree", ref, "--", path)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "", nil
	}
	meta, _, _ := strings.Cut(line, "\t")
	fields := strings.Fields(meta)
	if len(fields) < 3 {
		// git's own output, in a shape this parser does not know. Refuse
		// rather than return "" — "" means ABSENT to every caller, and
		// answering ABSENT from an unparsed line turns an update into an
		// "Add" and hides whatever is really there.
		return "", fmt.Errorf("git ls-tree %s -- %s: unparseable entry %q", ref, path, line)
	}
	return fields[2], nil
}

// Push pushes refspec to remote from dir.
func (execGit) Push(ctx context.Context, dir, remote, refspec string) error {
	return run(ctx, dir, "push", remote, refspec)
}

// RemoteRefSHA asks the remote what ref points at. Output shape:
//
//	0123abc…\trefs/heads/main
//
// Empty output means the remote has no such ref, which is a real answer and
// not an error.
func (execGit) RemoteRefSHA(ctx context.Context, dir, remote, ref string) (string, error) {
	out, err := output(ctx, dir, "ls-remote", remote, ref)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return "", nil
	}
	sha, _, found := strings.Cut(line, "\t")
	if !found || strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("git ls-remote %s %s: unparseable entry %q", remote, ref, line)
	}
	return strings.TrimSpace(sha), nil
}

// DiffPatch returns a unified diff of dir's tracked changes against HEAD.
func (execGit) DiffPatch(ctx context.Context, dir string) (string, error) {
	return output(ctx, dir, "diff", "HEAD")
}

// ListUntracked returns dir's untracked-but-not-ignored files, NUL-parsed so
// paths with spaces/newlines survive intact.
func (execGit) ListUntracked(ctx context.Context, dir string) ([]string, error) {
	out, err := output(ctx, dir, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// ApplyPatch applies patch to dir's working tree. An empty/whitespace-only
// patch is a deliberate no-op (there was nothing tracked to reproduce).
func (execGit) ApplyPatch(ctx context.Context, dir, patch string) (bool, error) {
	if strings.TrimSpace(patch) == "" {
		return false, nil
	}
	if _, err := outputStdin(ctx, dir, patch, "apply"); err != nil {
		return false, err
	}
	return true, nil
}

// defaultLogSinceMax bounds LogSince when the caller passes maxEntries<=0, so
// a caller that forgets to bound its own query still can't walk unbounded
// history.
const defaultLogSinceMax = 50

// logFieldSep separates the SHA/date/subject fields in LogSince's commit
// query. Chosen because it cannot appear in a commit subject typed at a
// terminal (it's a non-printable ASCII unit separator), unlike "|" or ",".
const logFieldSep = "\x1f"

// LogSince queries the commit list first (git log --pretty), then the
// changed-file list per commit (git diff-tree), rather than combining both
// into one --pretty + --name-only invocation: git interleaves file lists
// between commit records in that combined form with no field separator of
// its own, which is fragile to parse reliably. One extra process per commit
// is an acceptable cost given maxEntries bounds the total.
func (execGit) LogSince(ctx context.Context, dir string, since time.Time, maxEntries int) ([]LogEntry, error) {
	if maxEntries <= 0 {
		maxEntries = defaultLogSinceMax
	}
	args := []string{"log", fmt.Sprintf("--max-count=%d", maxEntries), "--date=iso-strict",
		"--pretty=format:%H" + logFieldSep + "%cI" + logFieldSep + "%s"}
	if !since.IsZero() {
		args = append(args, "--since="+since.UTC().Format(time.RFC3339))
	}
	out, err := output(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	entries := parseLogEntries(out)
	attachCommitFiles(ctx, dir, entries)
	return entries, nil
}

// attachCommitFiles fills in each entry's changed-file list, in place.
//
// Best-effort per commit: a file-list miss (e.g. an unreachable SHA under
// concurrent history rewrite) must not fail the whole query — the commit
// summary is still useful evidence without it. The miss is RECORDED
// (FilesUnknown) rather than swallowed, so a consumer cannot mistake it for
// a commit that touched nothing.
func attachCommitFiles(ctx context.Context, dir string, entries []LogEntry) {
	for i := range entries {
		filesOut, ferr := output(ctx, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", entries[i].SHA)
		if ferr != nil {
			entries[i].FilesUnknown = true
			continue
		}
		entries[i].Files = splitNonEmptyLines(filesOut)
	}
}

// parseLogEntries parses LogSince's --pretty=format output: one commit per
// line, fields separated by logFieldSep. A line that doesn't split into
// exactly three fields (never expected from git, but the input is still
// external process output) is skipped rather than producing a malformed
// entry.
func parseLogEntries(out string) []LogEntry {
	var entries []LogEntry
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, logFieldSep, 3)
		if len(parts) != 3 {
			continue
		}
		date, derr := time.Parse(time.RFC3339, parts[1])
		if derr != nil {
			date = time.Time{}
		}
		entries = append(entries, LogEntry{SHA: parts[0], Date: date, Subject: parts[2]})
	}
	return entries
}

// splitNonEmptyLines splits s on newlines, trimming and dropping blanks —
// the shape of `git diff-tree --name-only` output.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// parseWorktreeList parses `git worktree list --porcelain`. Records are separated
// by blank lines; each begins with a `worktree <path>` line and may carry HEAD,
// branch/detached, and bare markers.
func parseWorktreeList(out string) []Worktree {
	var (
		list []Worktree
		cur  *Worktree
	)
	flush := func() {
		if cur != nil {
			list = append(list, *cur)
			cur = nil
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			flush()
			cur = &Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case cur == nil:
			// Stray line before any worktree header — ignore.
		case strings.HasPrefix(line, "HEAD "):
			cur.Head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			cur.Branch = strings.TrimPrefix(line, "branch ")
		case line == "detached":
			cur.Detached = true
		case line == "bare":
			cur.Bare = true
		}
	}
	flush()
	return list
}

// run invokes git in dir, discarding stdout and folding stderr into any error.
// Mirrors internal/remote's runGit: per-command Dir (never os.Chdir), stderr
// captured, ctx cancellation surfaced, a missing binary reported clearly.
func run(ctx context.Context, dir string, args ...string) error {
	_, err := output(ctx, dir, args...)
	return err
}

// output invokes git in dir and returns its stdout, folding stderr into any error.
func output(ctx context.Context, dir string, args ...string) (string, error) {
	return outputStdin(ctx, dir, "", args...)
}

// outputStdin invokes git in dir with stdin fed to its standard input (empty
// stdin behaves exactly like output — most git subcommands never read it),
// returning stdout and folding stderr into any error. The stdin plumbing
// exists for subcommands that take their payload that way rather than as an
// argument (`git commit --file -`, `git apply`, both of which would
// otherwise hit argv length/quoting limits on a large diff or a multi-line
// commit message).
func outputStdin(ctx context.Context, dir, stdin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = gitutil.SanitizedEnviron()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		label := strings.Join(args, " ")
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("git binary not found on PATH: %w", err)
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("git %s: %w", label, ctx.Err())
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %s: %w", label, msg, err)
		}
		return "", fmt.Errorf("git %s: %w", label, err)
	}
	return stdout.String(), nil
}

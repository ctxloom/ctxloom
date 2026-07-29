// Package gitignore provides idempotent maintenance of a project's .gitignore,
// appending ctxloom-managed ignore patterns without disturbing user entries.
package gitignore

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Comment is the header under which ctxloom's ignore patterns are grouped.
const Comment = "# ctxloom private working state (rebuildable/local — cache, fetched pieces, sessions, ephemeral scratch, project id)"

// PrivateStatePatterns are the .ctxloom paths that are rebuildable or purely
// local and so must never ride a distributable tree: the resolved-artifact
// cache, fetched remote-bundle pieces, per-project session state, ephemeral
// scratch (synced bundles, transient context data), and the project-id
// marker (ADR 0025 — private identity). Everything else under .ctxloom/
// (config.yaml, remotes.yaml, lock.yaml, allowed_signers, approvals/,
// content/) is committed by omission — it's content, config, or trust state
// the project depends on.
var PrivateStatePatterns = []string{
	".ctxloom/cache/",
	".ctxloom/pieces/",
	".ctxloom/sessions/",
	".ctxloom/ephemeral/",
	".ctxloom/project-id",
}

// TransientArtifactPatterns are unambiguous generated artifacts that accumulate
// during hook application: the per-file settings backups, the Antigravity
// workspace directory, and the generated Codex project config. Scoped narrowly
// so files a content repo may legitimately track (.claude/commands/*.md,
// .mcp.json) stay the project's choice — .agents/ is ignored wholesale because
// besides ctxloom's generated hooks.json/mcp_config.json/skills, agy itself
// fills it with per-conversation subagent scratch that should never be
// committed, while for Codex only config.toml is generated, so only that file
// is ignored.
//
// .codex/auth.json (U045-F01): internal/lm/isolation/auth.go's SeedCodexHome
// actively copies the host's ~/.codex/auth.json here on every plain
// (non-isolated) codex run — a live credential, not a config artifact, but it
// must be kept out of the tree the exact same way.
var TransientArtifactPatterns = []string{"*.ctxloom.bak", ".agents/", ".codex/config.toml", ".codex/auth.json"}

// WorktreeComment is the header under which the per-agent-worktree exclude block
// is grouped in .git/info/exclude.
const WorktreeComment = "# ctxloom per-agent worktree config (isolation; NEVER merge back)"

// WorktreeArtifactPatterns is the BROADENED set of per-agent config artifacts a
// fan-out member materializes into its isolated worktree cwd — the FULL written
// set across all engines. Unlike TransientArtifactPatterns (which deliberately
// leaves .mcp.json/.claude/commands as the project's choice), this set must keep
// EVERY ctxloom-written config out of a developer member's merge-back, so it
// covers .mcp.json and .claude/ wholesale plus the cache. It is written to
// .git/info/exclude (untracked, common-dir) — NOT the tracked .gitignore, which
// would itself merge back. Safe: excludes only affect UNTRACKED files, so a repo
// that genuinely tracks .mcp.json is unaffected.
//
// CLAUDE.md and the root AGENTS.md belong here too (bony-carry, worktree
// orphan-accumulation fix): they are TRACKED per-agent context surfaces
// (claude.ClaudeCodeHookWriter.WriteContext, codex.CodexHookWriter.WriteContext
// — internal/shared/agent/managedcontext.go's doc names all three: CLAUDE.md,
// .agents/AGENTS.md, codex's AGENTS.md), and WriteManagedContext DELETES the
// file outright when the merged content is empty and the file was wholly
// ctxloom's. Omitting them here left isolation/worktree.go's
// skipTrackedConfig unable to hide that mutation: a per-agent run's
// materialize step turned a repo's committed CLAUDE.md into a tracked
// deletion (`git status` showed ` D CLAUDE.md`) that no skip-worktree bit
// covered, so teardown's WIP-safety check (correctly) read the worktree as
// dirty and refused `git worktree remove`, permanently orphaning it. agy's
// .agents/AGENTS.md is unaffected — already covered wholesale by the ".agents/"
// entry below.
var WorktreeArtifactPatterns = []string{
	".mcp.json",
	".claude/",
	".agents/",
	".codex/config.toml",
	".codex/auth.json",
	".kiro/",
	".ctxloom/cache/",
	"CLAUDE.md",
	"AGENTS.md",
	// U080-F09: opencode's own written artifacts (command/skill/context files
	// under .opencode/, its project-local opencode.json, and the managed-MCP
	// sidecar ledger) were missing from this set despite the doc comment above
	// calling it "the FULL written set across all engines" — an opencode-backed
	// per-agent worktree run left these untracked and unhidden.
	".opencode/",
	"opencode.json",
	".ctxloom-opencode-managed",
}

// SupersededPatterns are ignore rules written by older ctxloom versions that a
// current ctxloom must actively RETIRE rather than merely stop writing. A
// blanket .ctxloom/ rule predates version-controlled content moving INTO
// .ctxloom/ (content/, plus config.yaml, remotes.yaml and lock.yaml alongside
// it). Left in place it silently un-tracks the project's own content: `git add`
// reports nothing, and a content repo publishes an empty tree while every
// consumer's bundle refs fail to resolve. Ensure only ever appends, so no
// amount of re-running it can undo this — retirement must be explicit.
//
// Only the BLANKET forms belong here. The granular .ctxloom/<subdir>/ patterns
// in PrivateStatePatterns are current and must never be matched.
var SupersededPatterns = []string{".ctxloom", ".ctxloom/", "/.ctxloom", "/.ctxloom/"}

// supersededComments are ctxloom-authored headers that head nothing once their
// patterns are retired. Scoped to headers ctxloom itself wrote — a user's own
// comment is never removed, even if it sits above a retired rule.
var supersededComments = []string{"# ctxloom local files"}

// RetireSupersededFile removes any SupersededPatterns line (and any
// ctxloom-authored header left heading nothing) from path, reporting whether
// the file changed. An absent file is a no-op, not an error. Callers that
// write the private-state block should retire first, then Ensure.
func RetireSupersededFile(path string) (bool, error) {
	return retireBlock(path, supersededComments, SupersededPatterns)
}

// RetireWorktreeConfigBlock removes the WorktreeComment header and any
// WorktreeArtifactPatterns lines from the file at path — a git common-dir
// info/exclude. Worktree teardown calls this once no linked worktree remains
// that still needs the shared exclude block (U054-F02): the block is
// written into the repo's ONE shared common-dir file (git has no per-
// worktree info/exclude — verified: a linked worktree's own
// .git/worktrees/<name>/info/exclude is NOT honored by `git status`), so it
// cannot be scoped to a single worktree's teardown. It can only be safely
// removed once nothing else is relying on it, or it silently reappears as
// dirty/untracked noise for every OTHER worktree (including the developer's
// own main checkout) the moment it is gone.
func RetireWorktreeConfigBlock(path string) (bool, error) {
	return retireBlock(path, []string{WorktreeComment}, WorktreeArtifactPatterns)
}

// retireBlock is RetireSupersededFile's mechanism, generalized: remove any
// line matching one of patterns from the file at path, along with an
// orphaned header (one of headers) left immediately above a removed run. An
// absent file is a no-op, not an error.
func retireBlock(path string, headers, patterns []string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	retiredSet := make(map[string]bool, len(patterns))
	for _, p := range patterns {
		retiredSet[p] = true
	}
	orphanHeader := make(map[string]bool, len(headers))
	for _, h := range headers {
		orphanHeader[h] = true
	}

	lines := strings.Split(string(content), "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if retiredSet[trimmed] {
			// Drop a ctxloom-authored header immediately above it, which would
			// otherwise be left heading nothing.
			for len(kept) > 0 {
				last := strings.TrimSpace(kept[len(kept)-1])
				if orphanHeader[last] {
					kept = kept[:len(kept)-1]
					continue
				}
				break
			}
			continue
		}
		kept = append(kept, line)
	}

	updated := strings.Join(kept, "\n")
	if updated == string(content) {
		return false, nil
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return false, err
	}
	return true, nil
}

// Ensure appends the given patterns to projectDir/.gitignore under a single
// comment header, creating the file if absent. It is idempotent: only patterns
// not already present (by exact trimmed-line match) are written, and when none
// are missing the file is left untouched. An empty patterns list is a no-op.
//
// It first retires any SupersededPatterns. Retirement lives HERE, not at the
// call sites, because appending is powerless against a blanket .ctxloom/: git
// gives no way to re-include a path whose parent directory is excluded, so the
// granular patterns would be written and silently overridden, leaving the
// project's own .ctxloom/content/ invisible. Every writer of a project
// .gitignore must repair that rule, so no caller is given the chance to forget.
func Ensure(projectDir, comment string, patterns ...string) error {
	path := filepath.Join(projectDir, ".gitignore")

	retired, err := RetireSupersededFile(path)
	if err != nil {
		return err
	}
	if retired {
		// The blanket rule just removed WAS what kept private state out of git.
		// Install its replacement in the SAME block as the caller's patterns —
		// not every caller passes PrivateStatePatterns (the hook path passes only
		// transient artifacts), and retiring without replacing would leak cache/
		// and sessions/ into the repo. Retirement and its replacement are one
		// migration.
		patterns = dedupe(append(append([]string{}, PrivateStatePatterns...), patterns...))
		clidiag.WarnOnce("ctxloom",
			"removed a blanket .ctxloom/ rule from .gitignore: it predates version-controlled content living under .ctxloom/content/ and was hiding that content from git — replaced it with the granular private-state rules; review and commit the .gitignore change")
	}

	return EnsureFile(path, comment, patterns...)
}

// EnsureFile is Ensure targeting an arbitrary ignore file (e.g. a common-dir
// .git/info/exclude for per-agent worktrees), so the append/idempotency logic is
// written once. path's parent must already exist. An empty patterns list is a
// no-op.
func EnsureFile(path, comment string, patterns ...string) error {
	if len(patterns) == 0 {
		return nil
	}

	content, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	missing := missingPatterns(content, patterns)
	if len(missing) == 0 {
		return nil
	}
	return appendBlock(path, content, comment, missing)
}

// dedupe returns patterns with duplicates removed, preserving first-seen order.
// Needed when the private-state replacement is prepended to a caller's list that
// already contains it — missingPatterns dedupes against the FILE, not within the
// requested set, so a repeated pattern would otherwise be written twice.
func dedupe(patterns []string) []string {
	seen := make(map[string]bool, len(patterns))
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// missingPatterns returns the patterns not already present in content, matched
// by exact trimmed-line equality.
func missingPatterns(content []byte, patterns []string) []string {
	present := make(map[string]bool)
	for line := range strings.SplitSeq(string(content), "\n") {
		present[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, p := range patterns {
		if !present[p] {
			missing = append(missing, p)
		}
	}
	return missing
}

// appendBlock appends a comment header and the patterns to the file at path,
// inserting a separating newline when the existing content lacks a trailing one.
func appendBlock(path string, content []byte, comment string, patterns []string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	if err := writeBlock(f, content, comment, patterns); err != nil {
		_ = f.Close()
		return err
	}
	// U054-F04: a deferred, discarded Close() hides an ENOSPC/EDQUOT/EIO
	// write failure behind a nil error — the worst case being the migration
	// path in Ensure, which has already committed the REMOVAL of the
	// superseded blanket rule before this append runs; a silently-failed
	// append then leaves the project with FEWER ignore rules than before.
	return closeChecked(f, path)
}

// closeChecked closes f and, on failure, wraps the error naming path — split
// out so a test can drive Close-error propagation (via an already-closed
// *os.File) without needing to force a real ENOSPC/EDQUOT/EIO.
func closeChecked(f *os.File, path string) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("gitignore: writing %s: %w", path, err)
	}
	return nil
}

// writeBlock writes the separating newline (if needed), the comment header,
// and every pattern to w.
func writeBlock(w io.Writer, content []byte, comment string, patterns []string) error {
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := io.WriteString(w, "\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\n%s\n", comment); err != nil {
		return err
	}
	for _, p := range patterns {
		if _, err := fmt.Fprintf(w, "%s\n", p); err != nil {
			return err
		}
	}
	return nil
}

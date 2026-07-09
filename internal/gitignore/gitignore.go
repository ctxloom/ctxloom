// Package gitignore provides idempotent maintenance of a project's .gitignore,
// appending ctxloom-managed ignore patterns without disturbing user entries.
package gitignore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Comment is the header under which ctxloom's ignore patterns are grouped.
const Comment = "# ctxloom private working state (synced bundles, session/context, project id)"

// PrivateStatePatterns are the .ctxloom paths that must never ride a
// distributable tree: the ephemeral cache (synced bundles, session/context
// data) and the project-id marker (ADR 0025 — private identity).
var PrivateStatePatterns = []string{".ctxloom/ephemeral/", ".ctxloom/project-id"}

// TransientArtifactPatterns are unambiguous generated artifacts that accumulate
// during hook application: the per-file settings backups, the Antigravity
// workspace directory, and the generated Codex project config. Scoped narrowly
// so files a content repo may legitimately track (.claude/commands/*.md,
// .mcp.json) stay the project's choice — .agents/ is ignored wholesale because
// besides ctxloom's generated hooks.json/mcp_config.json/skills, agy itself
// fills it with per-conversation subagent scratch that should never be
// committed, while for Codex only config.toml is generated, so only that file
// is ignored.
var TransientArtifactPatterns = []string{"*.ctxloom.bak", ".agents/", ".codex/config.toml"}

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
var WorktreeArtifactPatterns = []string{
	".mcp.json",
	".claude/",
	".agents/",
	".codex/config.toml",
	".kiro/",
	".ctxloom/cache/",
}

// Ensure appends the given patterns to projectDir/.gitignore under a single
// comment header, creating the file if absent. It is idempotent: only patterns
// not already present (by exact trimmed-line match) are written, and when none
// are missing the file is left untouched. An empty patterns list is a no-op.
func Ensure(projectDir, comment string, patterns ...string) error {
	return EnsureFile(filepath.Join(projectDir, ".gitignore"), comment, patterns...)
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
	defer func() { _ = f.Close() }()

	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(f, "\n%s\n", comment); err != nil {
		return err
	}
	for _, p := range patterns {
		if _, err := fmt.Fprintf(f, "%s\n", p); err != nil {
			return err
		}
	}
	return nil
}

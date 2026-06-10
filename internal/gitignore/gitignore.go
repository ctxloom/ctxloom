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
// during hook application: the per-file settings backups, the generated Gemini
// harness directory, and the generated Codex project config. Scoped narrowly so
// files a content repo may legitimately track (.claude/commands/*.md, .mcp.json)
// stay the project's choice — .gemini/ is ignored wholesale because ctxloom
// generates the whole directory (settings.json plus exported commands), while
// for Codex only config.toml is generated, so only that file is ignored.
var TransientArtifactPatterns = []string{"*.ctxloom.bak", ".gemini/", ".codex/config.toml"}

// Ensure appends the given patterns to projectDir/.gitignore under a single
// comment header, creating the file if absent. It is idempotent: only patterns
// not already present (by exact trimmed-line match) are written, and when none
// are missing the file is left untouched. An empty patterns list is a no-op.
func Ensure(projectDir, comment string, patterns ...string) error {
	if len(patterns) == 0 {
		return nil
	}

	path := filepath.Join(projectDir, ".gitignore")
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

package backends

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
)

// SCMCommandsDir is the subdirectory within .claude/commands/ for SCM-generated commands.
const SCMCommandsDir = "scm"

// WriteCommandFiles generates Claude Code slash command files from prompts.
// It deletes the .claude/commands/scm/ directory and regenerates it fresh.
// Only prompts with ClaudeCode.IsEnabled() == true are exported.
func WriteCommandFiles(workDir string, prompts []*bundles.LoadedContent) error {
	scmDir := filepath.Join(workDir, ".claude", "commands", SCMCommandsDir)

	// Clean slate - remove and recreate
	if err := os.RemoveAll(scmDir); err != nil {
		return fmt.Errorf("remove scm commands dir: %w", err)
	}

	// Check if we have any prompts to export
	hasExportable := false
	for _, p := range prompts {
		if p.Plugins.LM.ClaudeCode.IsEnabled() {
			hasExportable = true
			break
		}
	}

	// Only create directory if we have prompts to export
	if !hasExportable {
		return nil
	}

	if err := os.MkdirAll(scmDir, 0755); err != nil {
		return fmt.Errorf("create scm commands dir: %w", err)
	}

	for _, p := range prompts {
		if !p.Plugins.LM.ClaudeCode.IsEnabled() {
			continue // Explicitly disabled
		}

		md := TransformToClaudeCommand(p)
		path := filepath.Join(scmDir, p.Name+".md")

		// Ensure parent directory exists for nested prompt names
		if dir := filepath.Dir(path); dir != scmDir {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create command subdir %s: %w", dir, err)
			}
		}

		if err := os.WriteFile(path, []byte(md), 0644); err != nil {
			return fmt.Errorf("write command %s: %w", p.Name, err)
		}
	}

	return nil
}

// TransformToClaudeCommand converts an SCM prompt to Claude Code command format.
// It generates a markdown file with YAML frontmatter and transforms {{var}} to $N.
func TransformToClaudeCommand(p *bundles.LoadedContent) string {
	var buf bytes.Buffer
	cc := p.Plugins.LM.ClaudeCode

	// Build frontmatter if we have any metadata
	hasFrontmatter := cc.Description != "" ||
		cc.ArgumentHint != "" ||
		len(cc.AllowedTools) > 0 ||
		cc.Model != ""

	if hasFrontmatter {
		buf.WriteString("---\n")

		if cc.Description != "" {
			buf.WriteString("description: ")
			buf.WriteString(escapeYAMLString(cc.Description))
			buf.WriteString("\n")
		}

		if cc.ArgumentHint != "" {
			buf.WriteString("argument-hint: ")
			buf.WriteString(cc.ArgumentHint)
			buf.WriteString("\n")
		}

		if len(cc.AllowedTools) > 0 {
			buf.WriteString("allowed-tools: ")
			buf.WriteString(strings.Join(cc.AllowedTools, ", "))
			buf.WriteString("\n")
		}

		if cc.Model != "" {
			buf.WriteString("model: ")
			buf.WriteString(cc.Model)
			buf.WriteString("\n")
		}

		buf.WriteString("---\n\n")
	}

	// Transform content: detect {{var}} patterns, assign $N by first occurrence
	content := transformMustacheToPositional(p.Content)
	buf.WriteString(content)

	return buf.String()
}

// transformMustacheToPositional replaces {{variable}} patterns with $1, $2, etc.
// Variables are assigned positions based on their first occurrence order.
func transformMustacheToPositional(content string) string {
	varNum := 1
	seen := make(map[string]int)
	re := regexp.MustCompile(`\{\{(\w+)\}\}`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		varName := re.FindStringSubmatch(match)[1]
		if num, exists := seen[varName]; exists {
			return fmt.Sprintf("$%d", num)
		}
		seen[varName] = varNum
		num := varNum
		varNum++
		return fmt.Sprintf("$%d", num)
	})
}

// escapeYAMLString escapes a string for safe inclusion in YAML.
// If the string contains special characters, it's quoted.
func escapeYAMLString(s string) string {
	// Check if string needs quoting
	needsQuotes := strings.ContainsAny(s, ":#{}[]&*!|>'\"%@`") ||
		strings.HasPrefix(s, " ") ||
		strings.HasSuffix(s, " ") ||
		strings.Contains(s, "\n")

	if needsQuotes {
		// Use double quotes and escape internal quotes
		escaped := strings.ReplaceAll(s, `"`, `\"`)
		return `"` + escaped + `"`
	}
	return s
}

package claude

import (
	"bytes"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
)

// renderCommand is claude's slash-command file form for one command: the
// one transform both the project's .claude/commands and the session home's
// commands directory go through (kit.DeliverCommands writes it). A nested
// name's path separators become dashes.
func renderCommand(c agent.CommandExport) (string, []byte, error) {
	filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
	return filename, []byte(TransformToClaudeCommand(c)), nil
}

// TransformToClaudeCommand converts a command export to Claude Code command
// format: a markdown file with YAML frontmatter and {{var}} transformed to $N.
func TransformToClaudeCommand(c agent.CommandExport) string {
	var buf bytes.Buffer

	hasFrontmatter := c.Description != "" ||
		c.ArgumentHint != "" ||
		len(c.AllowedTools) > 0 ||
		c.Model != ""

	if hasFrontmatter {
		buf.WriteString("---\n")

		if c.Description != "" {
			buf.WriteString("description: ")
			buf.WriteString(agent.EscapeYAMLString(c.Description))
			buf.WriteString("\n")
		}

		if c.ArgumentHint != "" {
			buf.WriteString("argument-hint: ")
			buf.WriteString(c.ArgumentHint)
			buf.WriteString("\n")
		}

		if len(c.AllowedTools) > 0 {
			buf.WriteString("allowed-tools: ")
			buf.WriteString(strings.Join(c.AllowedTools, ", "))
			buf.WriteString("\n")
		}

		if c.Model != "" {
			buf.WriteString("model: ")
			buf.WriteString(c.Model)
			buf.WriteString("\n")
		}

		buf.WriteString("---\n\n")
	}

	buf.WriteString(agent.TransformMustacheToPositional(c.Content))
	return buf.String()
}

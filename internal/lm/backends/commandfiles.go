package backends

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/spf13/afero"
)

// CommandFileOption configures command file writing.
type CommandFileOption func(*commandFileOptions)

type commandFileOptions struct {
	fs afero.Fs
}

// WithCommandFS sets the filesystem for command file operations.
func WithCommandFS(fs afero.Fs) CommandFileOption {
	return func(o *commandFileOptions) {
		o.fs = fs
	}
}

// WriteCommandFilesFor writes command files for the specified backend.
// It dispatches to the appropriate backend-specific writer.
func WriteCommandFilesFor(backendName, workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	switch backendName {
	case "claude-code":
		return WriteCommandFiles(workDir, prompts, opts...)
	case "gemini":
		return WriteGeminiCommandFiles(workDir, prompts, opts...)
	default:
		// Backend doesn't support command files, silently succeed
		return nil
	}
}

// WriteCommandFiles generates Claude Code slash command files from prompts.
// Files are written directly to .claude/commands/ (e.g., save.md -> /save).
// ctxloom tracks which files it manages via a manifest to clean up stale commands.
// Only prompts with ClaudeCode.IsEnabled() == true are exported.
func WriteCommandFiles(workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	options := &commandFileOptions{fs: afero.NewOsFs()}
	for _, opt := range opts {
		opt(options)
	}
	fs := options.fs

	commandsDir := filepath.Join(workDir, ".claude", "commands")
	manifestPath := filepath.Join(commandsDir, ".ctxloom-manifest")

	cleanupTrackedCommands(fs, commandsDir, manifestPath)

	if !hasExportableClaudePrompts(prompts) {
		_ = fs.Remove(manifestPath)
		return nil
	}

	if err := fs.MkdirAll(commandsDir, 0755); err != nil {
		return fmt.Errorf("create commands dir: %w", err)
	}

	manifest, err := writeClaudeCommands(fs, commandsDir, prompts)
	if err != nil {
		return err
	}

	// Write manifest for cleanup on next run
	if err := afero.WriteFile(fs, manifestPath, []byte(strings.Join(manifest, "\n")), 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	return nil
}

// cleanupTrackedCommands removes the old ctxloom/ subdirectory (migration) and
// every command file recorded in the previous run's manifest.
func cleanupTrackedCommands(fs afero.Fs, commandsDir, manifestPath string) {
	_ = fs.RemoveAll(filepath.Join(commandsDir, "ctxloom"))

	data, err := afero.ReadFile(fs, manifestPath)
	if err != nil {
		return
	}
	for _, name := range strings.Split(string(data), "\n") {
		if name = strings.TrimSpace(name); name != "" {
			_ = fs.Remove(filepath.Join(commandsDir, name))
		}
	}
}

// hasExportableClaudePrompts reports whether any prompt is enabled for Claude Code.
func hasExportableClaudePrompts(prompts []*bundles.LoadedContent) bool {
	for _, p := range prompts {
		if p.Plugins.LM.ClaudeCode.IsEnabled() {
			return true
		}
	}
	return false
}

// writeClaudeCommands writes each enabled prompt as a Claude command file,
// returning the manifest of written filenames.
func writeClaudeCommands(fs afero.Fs, commandsDir string, prompts []*bundles.LoadedContent) ([]string, error) {
	var manifest []string
	for _, p := range prompts {
		if !p.Plugins.LM.ClaudeCode.IsEnabled() {
			continue // Explicitly disabled
		}

		md := TransformToClaudeCommand(p)
		// Replace path separators with dashes for nested names.
		filename := strings.ReplaceAll(p.Name, "/", "-") + ".md"
		if err := afero.WriteFile(fs, filepath.Join(commandsDir, filename), []byte(md), 0644); err != nil {
			return nil, fmt.Errorf("write command %s: %w", p.Name, err)
		}
		manifest = append(manifest, filename)
	}
	return manifest, nil
}

// TransformToClaudeCommand converts a ctxloom prompt to Claude Code command format.
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

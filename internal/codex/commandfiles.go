package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// codexManifest tracks which prompt files ctxloom wrote, so cleanup removes only
// ctxloom-managed prompts and leaves the user's own untouched (the prompts dir
// is shared).
const codexManifest = ".ctxloom-manifest"

// codexHome resolves Codex's home directory: $CODEX_HOME if set (it IS the
// .codex dir, not its parent), else ~/.codex. This is the single source of
// truth for Codex-home precedence; codexPromptsDir, MCPRegistrar.ConfigPath
// (global scope), and getSessionsDir all resolve through it so they stay in
// lockstep with how codex itself locates its home.
func codexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// codexPromptsDir resolves Codex's custom-prompts directory. NOTE: unlike
// claude/gemini (project-scoped command dirs), Codex only discovers prompts from
// the GLOBAL, top-level $CODEX_HOME/prompts (default ~/.codex/prompts) — there is
// no project-level prompts dir. So codex slash commands are global: a session's
// Setup rewrites them, and the ctxloom manifest scopes cleanup to ctxloom's own
// files. Resolution mirrors the session-history dir: $CODEX_HOME, else ~/.codex.
func codexPromptsDir() string {
	home, err := codexHome()
	if err != nil {
		return filepath.Join(".codex", "prompts") // last-resort relative
	}
	return filepath.Join(home, "prompts")
}

// WriteCommandFiles generates Codex custom-prompt files from exported prompts.
// Files are written flat to the global $CODEX_HOME/prompts (e.g. save.md ->
// /save), since Codex scans only top-level markdown there. workDir is unused
// (codex prompts are global, not project-scoped — see codexPromptsDir). ctxloom
// tracks the files it manages via a manifest so it can clean up stale prompts
// without touching the user's own (see agent.WriteManagedCommandFiles for the
// shared mechanics). Only exports with Enabled == true are written.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	return agent.WriteManagedCommandFiles(fs, codexPromptsDir(), codexManifest, cmds,
		func(c agent.CommandExport) (string, []byte, error) {
			filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
			return filename, []byte(TransformToCodexPrompt(c)), nil
		})
}

// TransformToCodexPrompt converts a command export to a Codex prompt: optional
// YAML frontmatter (description + argument-hint, the keys Codex supports) plus a
// markdown body with {{var}} transformed to positional $N arguments (Codex's
// prompt argument syntax). Codex does not support allowed-tools/model frontmatter,
// so those export fields are dropped.
func TransformToCodexPrompt(c agent.CommandExport) string {
	var buf bytes.Buffer

	if c.Description != "" || c.ArgumentHint != "" {
		buf.WriteString("---\n")
		if c.Description != "" {
			buf.WriteString("description: ")
			buf.WriteString(agent.EscapeYAMLString(c.Description))
			buf.WriteString("\n")
		}
		if c.ArgumentHint != "" {
			buf.WriteString("argument-hint: ")
			buf.WriteString(agent.EscapeYAMLString(c.ArgumentHint))
			buf.WriteString("\n")
		}
		buf.WriteString("---\n\n")
	}

	buf.WriteString(agent.TransformMustacheToPositional(c.Content))
	return buf.String()
}

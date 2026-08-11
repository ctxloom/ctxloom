package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// codexHome resolves Codex's home directory: $CODEX_HOME if set (it IS the
// .codex dir, not its parent), else ~/.codex. This is the single source of
// truth for Codex-home precedence; MCPRegistrar.ConfigPath (global scope) and
// getSessionsDir both resolve through it so they stay in lockstep with how
// codex itself locates its home.
func codexHome() (string, error) {
	if home := os.Getenv(CodexHomeEnv); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName), nil
}

// GlobalHome returns codex's home resolution with NO workDir context —
// codexHome()'s own precedence ($CODEX_HOME, else ~/.codex) — exported for
// the codex descriptor's hookGlobalScopePaths (comfy-lion: the codex
// analog of prim-guy's claude $HOME-collision guard).
func GlobalHome() (string, error) { return codexHome() }

// ProjectHome returns the project-scoped $CODEX_HOME both the static
// apply/materialize path and the run path's in-tree arm target for workDir —
// cellScopedCodexHome under StateHome — exported for the same external
// collision check as GlobalHome.
//
// Since the home moved into the state tier that collision (workDir == $HOME,
// making the project home resolve onto codex's global one) is no longer
// REACHABLE: $HOME/.ctxloom/state/engines/codex/.codex is never $HOME/.codex.
// The guard stays wired anyway — it is a check on what these two functions
// return, not an assumption about what they cannot return, and the day a
// future engine's home resolves differently is not the day to discover the
// check was deleted.
func ProjectHome(workDir string) string { return cellScopedCodexHome(StateHome(workDir)) }

// codexPromptFile maps one command export to its Codex prompt file: a flat
// `<name>.md` (slashes flattened to dashes, since Codex scans only top-level
// markdown) whose bytes are TransformToCodexPrompt's rendering. It is the
// single source of truth for the prompt-file shape, used by the cell-scoped
// commands surface (surfaces.go).
func codexPromptFile(c agent.CommandExport) (string, []byte, error) {
	filename := strings.ReplaceAll(c.Name, "/", "-") + ".md"
	return filename, []byte(TransformToCodexPrompt(c)), nil
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

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

// hostCodexHome is the user's REAL codex home, ~/.codex, resolved WITHOUT
// consulting $CODEX_HOME. codexHome() deliberately honours that variable
// (that is codex's own precedence); this one deliberately does not, because
// its whole job is to answer "is the directory ctxloom is about to write the
// user's own home?" — a question $CODEX_HOME, which ctxloom itself sets,
// cannot be allowed to answer.
//
// Empty (with no error) is impossible: an unresolvable home dir returns the
// error, and callers treat that as "cannot tell", which is the safe answer for
// a check that only ever REFUSES writes.
func hostCodexHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName), nil
}

// IsHostCodexHome reports whether home — a resolved $CODEX_HOME, i.e. the
// .codex directory itself — is the user's own ~/.codex.
//
// It exists to enforce ONE rule: ctxloom never writes the engine's real host
// home. codex is the only engine where that rule needs enforcing, because it is
// the only one whose hooks/MCP/prompts/skills surfaces are home-keyed rather
// than cwd-keyed, so a run that keeps the real home has nowhere else for them
// to go. The answer is that they do not go anywhere — see surfaces.go's
// deliveryHome, which refuses and says so.
//
// Conservative by construction: anything it cannot resolve reports false, so a
// machine with no resolvable home directory degrades to writing a path that is
// definitionally not the user's real home rather than to refusing every write.
func IsHostCodexHome(home string) bool {
	real, err := hostCodexHome()
	if err != nil || real == "" || home == "" {
		return false
	}
	return cleanCodexPath(home) == cleanCodexPath(real)
}

// cleanCodexPath normalizes a path for comparison, falling back to Clean when
// it cannot be made absolute (filepath.Abs only fails when the process cwd
// itself cannot be determined).
func cleanCodexPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(p)
}

// GlobalHome returns codex's home resolution with NO workDir context —
// codexHome()'s own precedence ($CODEX_HOME, else ~/.codex) — exported for
// the codex descriptor's hookGlobalScopePaths (comfy-lion: the codex
// analog of prim-guy's claude $HOME-collision guard).
func GlobalHome() (string, error) { return codexHome() }

// ProjectHome returns the project-root codex home the HARPLESS static
// apply/materialize path targets for workDir — <workDir>/.codex — exported for
// the same external collision check as GlobalHome.
//
// S7 INTERIM, see CodexHookWriter.SettingsPath: the run path no longer resolves
// here at all. A run's CODEX_HOME is either a per-session instance
// (SessionHome, under `config_home: project`) or the user's real ~/.codex, and
// neither has a harpless spelling. This join is what the static writers shared
// before the retired durable per-project home existed.
//
// The collision it guards (workDir == $HOME making the project home resolve
// onto codex's global one — $HOME/.codex both ways) is REACHABLE again with
// this shape, which is precisely why the guard is wired.
func ProjectHome(workDir string) string { return cellScopedCodexHome(workDir) }

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

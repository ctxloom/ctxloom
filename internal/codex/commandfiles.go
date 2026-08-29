//go:build parked_engines

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
// Also read by internal/cli's DOCTOR-CHECK-CODEXHOME-n4, which REPORTS both
// homes: reading the real home is always permitted, only writing it is not.
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
// codexHome()'s own precedence ($CODEX_HOME, else ~/.codex).
//
// This is what codex reads for a run that keeps its own home: no agent
// binding, an undeclared one, or an explicit `config_home: host` (D2). It is
// therefore the home internal/cli's DOCTOR-CHECK-CODEXHOME-n4 reports as the
// host truth.
//
// ProjectHome, its former sibling, is DELETED with S7: there is no project-root
// codex home. Its one production reader was the codex descriptor's
// hookGlobalScopePaths $HOME-collision guard, which the declared absence
// retires — a surface that writes nothing cannot collide with anything.
func GlobalHome() (string, error) { return codexHome() }

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

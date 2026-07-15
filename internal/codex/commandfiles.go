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
// the top-level $CODEX_HOME/prompts (default ~/.codex/prompts) — there is
// no project-level prompts dir OF ITS OWN. So codex slash commands are always
// $CODEX_HOME-relative; whether that is "global" or "project-scoped" is
// entirely a question of what CODEX_HOME resolves to (an isolated run points
// it at a per-cell/per-agent home — see backend.go's resolveCodexProjectDir —
// so this same $CODEX_HOME/prompts convention is what makes those cell-scoped
// prompts.go). Resolution mirrors the session-history dir: $CODEX_HOME, else
// ~/.codex.
func codexPromptsDir() string {
	home, err := codexHome()
	if err != nil {
		return filepath.Join(".codex", "prompts") // last-resort relative
	}
	return filepath.Join(home, "prompts")
}

// promptsDirFor resolves the prompts dir a DIRECT (non-cell-scoped) caller of
// WriteCommandFiles targets, honouring the SAME precedence codexHome() itself
// uses plus a workDir fallback (comfy-lion, the prim-guy analog): an explicit
// $CODEX_HOME wins first (an isolated caller already has one set, and it must
// not be silently overridden); else, given a real workDir, the project-scoped
// cellScopedPromptsDir(workDir) — never the bare TRUE-global codexPromptsDir()
// — so a static caller with project context can't silently mutate
// ~/.codex/prompts for every project; else (no workDir at all) the last-resort
// global codexPromptsDir(). Shared by WriteCommandFiles/skillsDirFor's
// WriteSkillFiles sibling.
func promptsDirFor(workDir string) string {
	if os.Getenv("CODEX_HOME") != "" || workDir == "" {
		return codexPromptsDir()
	}
	return cellScopedPromptsDir(workDir)
}

// GlobalHome returns codex's home resolution with NO workDir context —
// codexHome()'s own precedence ($CODEX_HOME, else ~/.codex) — exported for
// operations/hooks.go's checkCodexHookTargetScope (comfy-lion: the codex
// analog of prim-guy's claude $HOME-collision guard).
func GlobalHome() (string, error) { return codexHome() }

// ProjectHome returns the cell-scoped $CODEX_HOME the static apply/materialize
// path targets for workDir (cellScopedCodexHome), exported for the same
// external collision check as GlobalHome.
func ProjectHome(workDir string) string { return cellScopedCodexHome(workDir) }

// WriteCommandFiles generates Codex custom-prompt files from exported prompts,
// targeting promptsDirFor(workDir) — see its doc for the precedence
// ($CODEX_HOME env, else workDir-scoped, else the bare global). Files are
// written flat (e.g. save.md -> /save), since Codex scans only top-level
// markdown there. ctxloom tracks the files it manages via a manifest so it can
// clean up stale prompts without touching the user's own (see
// agent.WriteManagedCommandFiles for the shared mechanics). Only exports with
// Enabled == true are written.
func WriteCommandFiles(workDir string, cmds []agent.CommandExport, opts ...agent.CommandFileOption) error {
	fs := agent.ResolveCommandFS(opts...)
	return agent.WriteManagedCommandFiles(fs, promptsDirFor(workDir), codexManifest, cmds, codexPromptFile)
}

// codexPromptFile maps one command export to its Codex prompt file: a flat
// `<name>.md` (slashes flattened to dashes, since Codex scans only top-level
// markdown) whose bytes are TransformToCodexPrompt's rendering. It is the single
// source of truth for the prompt-file shape, shared by WriteCommandFiles (global
// $CODEX_HOME/prompts) and the cell-scoped commands surface (surfaces.go) so the
// two can never drift.
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

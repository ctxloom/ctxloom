package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// backendName is claude's REGISTERED backend name (internal/lm/backends), the
// key credentialSeedSpecs and the backends registry are both spelled with. It
// scopes this engine's project-scoped home so no two engines can ever share
// one directory. A literal rather than an import of internal/config: this
// package is imported BY the registry, and the arch gate
// (tests/arch's engine-identity rosters) is where names are cross-checked.
const backendName = "claude-code"

// StateHome is the project-scoped config home ctxloom points claude at for
// workDir — <workDir>/.ctxloom/state/engines/claude-code — from which
// InTreeConfigDir derives the actual CLAUDE_CONFIG_DIR.
//
// It is the SINGLE owner of that location: the run path (the in-tree agent-home
// contribution in internal/operations) and any future home-keyed static writer
// resolve through it, so a `run` and a `materialize` can never target different
// roots — the writer split internal/codex.StateHome's doc describes, closed
// here before it can open.
//
// WHY NOT the real ~/.claude, which an in-tree claude run used before this
// existed: an AGENT run is not the human. Pointing it at the human's own home
// hands every delegated child the user's memory, plugins, MCP registrations and
// settings, and lets it write session state and hook edits back into them. A
// home ctxloom RELOCATES belongs in the state tier (gitignored, unrebuildable —
// paths.EngineStateHome) because it holds seeded credentials.
//
// The rule is AGENT runs only: the human's own interactive session keeps the
// real ~/.claude. See docs/architecture/engines/isolation.md's engine config
// homes section, and operations.InTreeAgentHomeEnv, which is the one place that
// condition is decided.
//
// claude's CWD-KEYED surfaces — CLAUDE.md, .claude/settings.json, .claude/
// commands, .claude/skills, .claude/agents — are untouched by this and stay at
// the project root where claude natively reads them.
func StateHome(workDir string) string {
	return paths.EngineStateHome(filepath.Join(workDir, paths.AppDirName), backendName)
}

// inTreeConfigLeaf is the directory INSIDE StateHome that CLAUDE_CONFIG_DIR
// names. It equals the leaf the WORKTREE axis uses (internal/lm/isolation's
// credentialSeedSpecs["claude-code"] destSubdir / HomeVars Subdir) so claude's
// config-home layout is the same shape on every axis, and — load-bearing — so
// isolation.SeedClaudeHome, which joins its OWN copy of that leaf under the
// root it is handed, writes .credentials.json into the exact directory this
// function names. The two literals live in different packages because isolation
// cannot import this one (claude -> internal/acp -> internal/lm/isolation is a
// real cycle); TestInTreeConfigDir_IsTheSeedDestination is the gate that keeps
// them equal.
const inTreeConfigLeaf = "claude"

// InTreeConfigDir is the CLAUDE_CONFIG_DIR value for an in-tree AGENT run in
// workDir. Seeded with the host's .credentials.json by
// isolation.SeedClaudeHome, which is handed StateHome(workDir) and appends the
// same leaf.
func InTreeConfigDir(workDir string) string {
	return filepath.Join(StateHome(workDir), inTreeConfigLeaf)
}

package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// inTreeConfigLeaf is the directory INSIDE the session's instance home
// (paths.SessionHomePath) that CLAUDE_CONFIG_DIR names. It equals the leaf the
// WORKTREE axis uses (internal/lm/isolation's credentialSeedSpecs
// ["claude-code"] destSubdir / HomeVars Subdir) so claude's config-home layout
// is the same shape on every axis, and — load-bearing — so
// isolation.PrepareClaudeHome, which joins its OWN copy of that leaf under the
// root it is handed, writes .credentials.json into the exact directory
// SessionConfigDir names. The two literals live in different packages because
// isolation cannot import this one (claude -> internal/acp ->
// internal/lm/isolation is a real cycle);
// TestSessionConfigDir_IsTheSeedDestination is the gate that keeps them equal.
const inTreeConfigLeaf = "claude"

// SessionConfigDir is the CLAUDE_CONFIG_DIR value for ONE SESSION's in-tree
// agent run in workDir — <workDir>/.ctxloom/state/<harp>/home/claude.
//
// PER SESSION, not per project. The instance is created at instance time,
// seeded one-way from the real host home, and disposable: two concurrent
// sessions in one checkout get two homes, and nothing an agent writes inside
// one ever reaches the human's ~/.claude. That real home stays the durable
// truth and ctxloom never writes it.
//
// WHY NOT the real ~/.claude, which an in-tree claude run used before the
// controlled home existed: an AGENT run is not the human. Pointing it at the
// human's own home hands every delegated child the user's memory, plugins, MCP
// registrations and settings, and lets it write session state and hook edits
// back into them.
//
// The rule is AGENT runs whose binding declares `config_home: project`: every
// other run — no binding, an undeclared binding, an explicit `host` — keeps the
// real ~/.claude. See docs/architecture/engines/isolation.md's engine config
// homes section, and operations.InTreeAgentHomeEnv, which is the one place that
// condition is decided.
//
// claude's CWD-KEYED surfaces — CLAUDE.md, .claude/settings.json, .claude/
// commands, .claude/skills, .claude/agents — are untouched by this and stay at
// the project root where claude natively reads them.
//
// The error is harp validation (paths.SessionStatePath): a harpless caller
// cannot resolve an instance at all, which is what keeps a durable
// project-wide home from quietly regrowing.
func SessionConfigDir(workDir, harp string) (string, error) {
	root, err := paths.SessionHomePath(filepath.Join(workDir, paths.AppDirName), harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, inTreeConfigLeaf), nil
}

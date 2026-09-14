package claude

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// HomeLeaf is the directory INSIDE a ctxloom-provisioned instance home
// (paths.SessionHomePath) that CLAUDE_CONFIG_DIR names. It is ONE constant on
// purpose: the engine's descriptor declares it as the home var's Subdir, so
// the seed internal/lm/isolation writes and the directory SessionConfigDir
// names are the same directory by construction, on every cell.
const HomeLeaf = "claude"

// SessionConfigDir is the CLAUDE_CONFIG_DIR value for ONE SESSION's agent run
// in workDir — <workDir>/.ctxloom/state/<harp>/home/claude — on the host; a
// container run is told the path that directory is mounted at instead.
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
// The rule is AGENT runs whose binding declares `engine_home: session`: every
// other run — no binding, an undeclared binding, an explicit `host` — keeps the
// real ~/.claude. See docs/architecture/engines/isolation.md's engine config
// homes section, and operations.ResolveInTreeAgentHome, which is the one place
// that condition is decided.
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
	return filepath.Join(root, HomeLeaf), nil
}

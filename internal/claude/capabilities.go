package claude

import (
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ClaudeCommands registers slash commands for Claude Code.
type ClaudeCommands struct{}

// RegisterFromContent writes slash commands from host-resolved command exports.
// The host maps bundle content (with claude-code enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *ClaudeCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// tough-cloud S5: ClaudeSessionHistory (the ~/.claude/projects/<encoded-cwd>/
// *.jsonl scraper — GetCurrentSession/ListSessions/GetSession/
// GetSessionByPath/TranscriptPathFromHook, its cwd->dir slug encoder
// (claudeProjectName), and its modern/legacy block parser — was DELETED
// outright, not demoted to an importer — the user's explicit decision
// (tall-grab: the pre-fix encoder derived a directory that does not exist for
// any workDir containing a dot/underscore/space, silently killing session
// history and recovery for those paths). Claude Code's structured Chat driver
// already streams the real conversation through the claude-code-acp ACP
// adapter, captured canonically instead (see internal/transcript); Claude's
// Backend.History() now returns nil.

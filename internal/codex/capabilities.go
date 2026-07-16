package codex

import (
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CodexCommands registers custom prompts for Codex CLI.
type CodexCommands struct{}

// RegisterFromContent writes custom prompts from host-resolved command exports.
// The host maps bundle content (with codex enablement + metadata) to these
// agent-agnostic exports, so this stays config/bundle-free.
func (s *CodexCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return WriteCommandFiles(workDir, cmds)
}

// tough-cloud S5: CodexSessionHistory (the $CODEX_HOME/sessions/YYYY/MM/DD/
// rollout-*.jsonl scraper — GetCurrentSession/ListSessions/GetSession/
// GetSessionByPath/TranscriptPathFromHook, its envelope-vs-flat entry parser,
// and the SessionHistory wiring in backend.go) was DELETED outright, not
// demoted to an importer — the user's explicit decision (lived-zone: this
// reader's envelope-vs-flat parsing mismatch made it silently return
// zero-entry sessions). Codex's structured Chat driver already streams the
// real conversation through ACP, captured canonically instead (see
// internal/transcript); Codex's Backend.History() now returns nil.

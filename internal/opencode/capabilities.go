package opencode

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file carries the placeholder launch capabilities the embedded
// agent.LaunchBackend requires (skills + session history). Slice 1 is the chat
// spine (chat.go); the setup-side capabilities are stubs until the later slices
// that materialize opencode's native config and read its session transcripts.

// opencodeSkills is a no-op ContentSkills. opencode reads .claude/skills/
// natively, so ctxloom writes no skill format of its own here in slice 1.
type opencodeSkills struct{}

func (s *opencodeSkills) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return nil
}

// opencodeSessionHistory is a placeholder SessionHistory. opencode persists its
// own session store (opencode export/session), but reading it back into
// ctxloom's transcript model is a later slice; until then history is unsupported.
type opencodeSessionHistory struct{}

func (h *opencodeSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return nil, fmt.Errorf("opencode session history not yet supported")
}

func (h *opencodeSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	return nil, nil
}

func (h *opencodeSessionHistory) GetSession(workDir, sessionID string) (*agent.Session, error) {
	return nil, fmt.Errorf("opencode session history not yet supported")
}

func (h *opencodeSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, fmt.Errorf("opencode session history not yet supported")
}

func (h *opencodeSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

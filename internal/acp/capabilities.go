package acp

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// This file carries the placeholder launch capabilities the embedded
// agent.LaunchBackend requires (commands + session history). The generic ACP
// backend's job in this increment is the StructuredChat driver (session.go); the
// setup-side capabilities are stubs until config-materialization delegation to
// the target agent is designed (see doc.go's registration TODO).

// acpCommands is a no-op ContentCommands: a generic ACP client has no native
// command (slash-command) format of its own — that belongs to the target
// agent, so command materialization will delegate there once registration is
// wired.
type acpCommands struct{}

func (s *acpCommands) RegisterFromContent(workDir string, cmds []agent.CommandExport) error {
	return nil
}

// acpSessionHistory is a placeholder SessionHistory for the ACP backend.
//
// ACP exposes conversation state over the live `session/update` stream rather
// than a client-side transcript file, and this increment does not persist one.
// /clear recovery + compaction over ACP (e.g. reconstructing a transcript from
// the update stream, or the agent's own `session/load`) is a later phase; until
// then history access is unsupported.
type acpSessionHistory struct{}

func (h *acpSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return nil, fmt.Errorf("acp session history not yet supported")
}

func (h *acpSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	return nil, nil
}

func (h *acpSessionHistory) GetSession(workDir, sessionID string) (*agent.Session, error) {
	return nil, fmt.Errorf("acp session history not yet supported")
}

func (h *acpSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, fmt.Errorf("acp session history not yet supported")
}

func (h *acpSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

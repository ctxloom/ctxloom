package backends

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// NilSessionHistory is a no-op SessionHistory for backends that don't support
// history access (e.g. mock).
type NilSessionHistory struct{}

// GetCurrentSession returns an error indicating history is not supported.
func (h *NilSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// ListSessions returns an empty list.
func (h *NilSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	return nil, nil
}

// GetSession returns an error indicating history is not supported.
func (h *NilSessionHistory) GetSession(workDir string, sessionID string) (*agent.Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// GetSessionByPath returns an error indicating history is not supported.
func (h *NilSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, fmt.Errorf("session history not supported by this backend")
}

// TranscriptPathFromHook returns empty string - no history support.
func (h *NilSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

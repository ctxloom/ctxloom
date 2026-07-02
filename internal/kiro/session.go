package kiro

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// kiroSessionHistory is a placeholder SessionHistory for the Kiro backend.
//
// Kiro persists sessions per working directory as a triple under KIRO_HOME (or
// ~/.kiro): {session_id}.json (metadata), {session_id}.jsonl (append-only
// conversation log), and {session_id}.lock. The jsonl log generalizes the
// existing JSONL transcript parser.
//
// TODO(phase-3): parse the jsonl transcripts for /clear recovery + compaction,
// and map harp names → session ids (Kiro has no --name; use --resume-id /
// --list-sessions --format json). Until then history access is unsupported.
type kiroSessionHistory struct{}

func (h *kiroSessionHistory) GetCurrentSession(workDir string) (*agent.Session, error) {
	return nil, fmt.Errorf("kiro session history not yet supported")
}

func (h *kiroSessionHistory) ListSessions(workDir string) ([]agent.SessionMeta, error) {
	return nil, nil
}

func (h *kiroSessionHistory) GetSession(workDir, sessionID string) (*agent.Session, error) {
	return nil, fmt.Errorf("kiro session history not yet supported")
}

func (h *kiroSessionHistory) GetSessionByPath(path string) (*agent.Session, error) {
	return nil, fmt.Errorf("kiro session history not yet supported")
}

func (h *kiroSessionHistory) TranscriptPathFromHook(workDir, sessionID, transcriptPath string) string {
	return ""
}

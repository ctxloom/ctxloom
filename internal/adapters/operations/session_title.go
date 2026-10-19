package operations

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// SessionTitle is a session's one-line title: its first main-thread user
// prompt, read from ctxloom's canonical transcript when it is listed, cut to
// its first non-empty line and 80 characters (sessions.FirstLineSummary).
//
// It is computed, never stored: the transcript is the record, and the title
// costs one parse per listed row. A session with no canonical transcript, an
// unreadable one, or one in which the user has not yet said anything has no
// title ("") — a renderer chooses its own placeholder. A subagent's prompt
// (a sidechain entry) is not the user's and never titles a session.
func SessionTitle(e sessions.Entry) string {
	if e.CanonicalTranscriptPath == "" {
		return ""
	}
	sess, err := transcript.ParseTranscriptFile(e.CanonicalTranscriptPath, e.HarpName)
	if err != nil {
		return ""
	}
	for _, entry := range agent.MainThreadEntries(sess.Entries) {
		if entry.Type != agent.EntryTypeUser || strings.TrimSpace(entry.Content) == "" {
			continue
		}
		return sessions.FirstLineSummary(entry.Content)
	}
	return ""
}

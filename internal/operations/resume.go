package operations

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// RecordedSessionEntries resolves a harp to its recorded transcript entries
// via the owning backend's session reader (the plugin reassembles and
// normalizes its own native transcript). Shared by the ACP resume path and
// the coordinator's ended-child resume.
func RecordedSessionEntries(ctx context.Context, harp string) ([]agent.SessionEntry, error) {
	entry, err := GetSession(harp)
	if err != nil {
		return nil, fmt.Errorf("unknown session %q: %w", harp, err)
	}
	if entry.SessionID == "" {
		return nil, fmt.Errorf("session %q has no bound transcript to load", harp)
	}
	sess, err := pb.NewSessionReader(entry.Backend, 0).GetSession(ctx, entry.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", harp, err)
	}
	// Replay primes the resumed engine with the conversation the user had —
	// subagent-interior (sidechain) entries stay out of it.
	return agent.MainThreadEntries(sess.Entries), nil
}

// resumeTranscriptBudget caps how much rendered history primes the resumed
// engine — the TAIL wins (most recent exchange matters most for continuation).
const resumeTranscriptBudget = 32 * 1024

// RenderResumedTranscript renders recorded entries as a lead-block section the
// fresh engine can continue from. Only conversation substance is included
// (user/assistant text and tool-call names); thinking, tool output, and system
// entries are bulk without continuation value.
func RenderResumedTranscript(harp string, entries []agent.SessionEntry) string {
	var parts []string
	for _, e := range entries {
		switch e.Type {
		case agent.EntryTypeUser:
			if e.Content != "" {
				parts = append(parts, "user: "+e.Content)
			}
		case agent.EntryTypeAssistant:
			if e.Content != "" {
				parts = append(parts, "assistant: "+e.Content)
			}
		case agent.EntryTypeToolUse:
			if e.ToolName != "" {
				parts = append(parts, "assistant ran tool: "+e.ToolName)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}

	start, total := len(parts), 0
	for i := len(parts) - 1; i >= 0; i-- {
		total += len(parts[i]) + 2
		if total > resumeTranscriptBudget {
			break
		}
		start = i
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Resumed session %s\n\nThis conversation continues a previous session; its recorded history follows.\n\n", harp)
	if start > 0 {
		fmt.Fprintf(&b, "(earlier history truncated: %d entries omitted)\n\n", start)
	}
	b.WriteString(strings.Join(parts[start:], "\n\n"))
	return b.String()
}

// JoinLeadBlocks joins first-turn lead blocks, dropping empties.
func JoinLeadBlocks(blocks ...string) string {
	var nonEmpty []string
	for _, b := range blocks {
		if b != "" {
			nonEmpty = append(nonEmpty, b)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

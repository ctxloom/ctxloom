package operations

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// RecordedSessionEntries resolves a harp to its recorded transcript entries
// via the owning backend's session reader (the plugin reassembles and
// normalizes its own native transcript). Shared by the ACP resume path and
// the coordinator's ended-child resume.
//
// Every failure here is an ERROR, never a panic and never a silent empty
// slice: the three callers all treat an error as "warn and carry on with the
// context you have" (see cli/run.go's resumeFullContext), and a typo'd or
// stale --session must never block a launch.
func RecordedSessionEntries(ctx context.Context, harp string) ([]agent.SessionEntry, error) {
	entry, err := GetSession(harp)
	if err != nil {
		return nil, fmt.Errorf("look up session %q: %w", harp, err)
	}
	// GetSession reports an ABSENT harp as (nil, nil) — "no such entry" is not
	// an error to it. Dereferencing without this check is what turned a
	// mistyped harp (three random words; the most ordinary mistake available
	// on this command) into a nil-pointer stack trace, and it did so on the
	// shared primitive, so the ACP resume path and the coordinator's
	// ended-child resume inherited the same crash.
	if entry == nil {
		return nil, fmt.Errorf("unknown session %q: no recorded session by that name; `ctxloom session list` shows the ones there are", harp)
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
		// A resolved, error-free harp whose recorded transcript
		// has no substantive entries used to prime the resumed engine with
		// ZERO bytes of history and give no signal beyond a plain "". Warn
		// here — once — so all three consumers (engine_session.go's ACP
		// resume, coord/spawner.go's ResumeContext, cli/run.go's
		// resumeFullContext) get it automatically instead of each needing
		// its own rendered=="" check.
		clidiag.Warn("ctxloom", "resumed session %s: recorded transcript has no user/assistant/tool-use entries to prime; resuming with no prior history", harp)
		return ""
	}

	// The tail always includes at least the LAST entry: start
	// used to be initialized to len(parts) and only advanced INSIDE the
	// budget-tested loop, so a single trailing entry alone bigger than the
	// budget broke on its first iteration before start ever moved — the
	// body was empty while the header still announced "history follows". If
	// even that last entry overruns the budget by itself, its own text is
	// truncated rather than the whole entry being dropped.
	last := parts[len(parts)-1]
	if len(last) > resumeTranscriptBudget {
		last = last[:resumeTranscriptBudget] + "\n… (entry truncated)"
		parts[len(parts)-1] = last
	}

	start, total := len(parts)-1, len(last)+2
	for i := start - 1; i >= 0; i-- {
		if total+len(parts[i])+2 > resumeTranscriptBudget {
			break
		}
		total += len(parts[i]) + 2
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

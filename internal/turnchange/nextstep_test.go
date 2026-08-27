package turnchange

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	claudereader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/claude"
	codexreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/codex"
)

// assistantTextLine is an assistant message carrying TEXT rather than a tool
// call — what a turn ends with, and the thing next-step capture reads.
func assistantTextLine(uuid, msgID, text string, sidechain bool) string {
	return `{"type":"assistant","isSidechain":` + boolStr(sidechain) + `,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"text","text":"` + text +
		`"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:02.000Z"}`
}

func ev(t agent.SessionEntryType, content string, sidechain bool) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{Type: t, Content: content, Sidechain: sidechain}}
}

// TestLastAssistantText_TakesTheLastOneInTheTurn pins that the LAST assistant
// message wins. Capture overwrites per turn, so what the turn finally said is
// the statement of intent — an earlier one in the same turn was superseded.
//
// MUTATION — walk the turn forwards instead of backwards in LastAssistantText —
// turns this red: it would return the first message rather than the last.
func TestLastAssistantText_TakesTheLastOneInTheTurn(t *testing.T) {
	got := LastAssistantText([]agent.ChatEvent{
		ev(agent.EntryTypeUser, "do the thing", false),
		ev(agent.EntryTypeAssistant, "first, some thinking", false),
		ev(agent.EntryTypeToolUse, "", false),
		ev(agent.EntryTypeAssistant, "Next I will run the gates.", false),
	})
	assert.Equal(t, "Next I will run the gates.", got)
}

// TestLastAssistantText_IsScopedToTheCurrentTurn pins that a message from an
// EARLIER turn is never returned. A stale intention re-presented as the
// current one is worse than no intention: it would steer the distiller toward
// work the session has already moved past.
//
// MUTATION — drop the CurrentTurn(evs) call and scan all of evs — turns this
// red, because the previous turn's message would be found.
func TestLastAssistantText_IsScopedToTheCurrentTurn(t *testing.T) {
	got := LastAssistantText([]agent.ChatEvent{
		ev(agent.EntryTypeUser, "first ask", false),
		ev(agent.EntryTypeAssistant, "STALE from the previous turn", false),
		ev(agent.EntryTypeUser, "second ask", false),
		ev(agent.EntryTypeToolUse, "", false),
	})
	assert.Empty(t, got, "a turn that produced no assistant message must yield nothing, not the last turn's")
}

// TestLastAssistantText_IgnoresSidechainMessages pins the subagent exclusion: a
// child's closing words are its report to this agent, not this agent's own
// statement of what it intends to do next.
//
// MUTATION — drop the `|| e.Sidechain` condition — turns this red.
func TestLastAssistantText_IgnoresSidechainMessages(t *testing.T) {
	got := LastAssistantText([]agent.ChatEvent{
		ev(agent.EntryTypeUser, "delegate it", false),
		ev(agent.EntryTypeAssistant, "the coordinator's own next step", false),
		ev(agent.EntryTypeAssistant, "SUBAGENT REPORT: I finished", true),
	})
	assert.Equal(t, "the coordinator's own next step", got)
}

// TestLastAssistantText_SkipsEmptyAssistantMessages pins that a trailing
// content-free assistant entry (a tool-only response) does not shadow the real
// message behind it — otherwise WriteNextStep would be handed "" and the
// capture would be refused for a turn that did say something.
//
// MUTATION — return e.Content without the TrimSpace/non-empty check — red.
func TestLastAssistantText_SkipsEmptyAssistantMessages(t *testing.T) {
	got := LastAssistantText([]agent.ChatEvent{
		ev(agent.EntryTypeUser, "go", false),
		ev(agent.EntryTypeAssistant, "the real statement", false),
		ev(agent.EntryTypeAssistant, "   \n ", false),
	})
	assert.Equal(t, "the real statement", got)
}

// TestReadTranscript_ReadsTheVendorFormatEndToEnd is the end-to-end half:
// it proves the exported reader and LastAssistantText compose over the REAL
// vendor adapter, not just over hand-built events.
//
// MUTATION — have ReadTranscript return c.events[:0] — turns this red.
func TestReadTranscript_ReadsTheVendorFormatEndToEnd(t *testing.T) {
	p := writeTranscript(t,
		promptLine("capture my next step", "u1"),
		assistantToolLine("a1", "msg_1", "Read", `{"file_path":"/plan.md"}`, false),
		assistantTextLine("a2", "msg_2", "Next I will merge the branch.", false),
	)

	evs, err := ReadTranscript(context.Background(), claudereader.Adapter{}, p)
	require.NoError(t, err)
	require.NotEmpty(t, evs, "the vendor transcript must yield events")

	assert.Equal(t, "Next I will merge the branch.", LastAssistantText(evs))
}

// TestReadTranscript_UnreadableFileErrors pins that a read failure is
// REPORTED rather than reported as an empty transcript — the caller must be
// able to say why no next step was captured.
//
// MUTATION — swallow the Convert error and return (c.events, nil) — red.
func TestReadTranscript_UnreadableFileErrors(t *testing.T) {
	_, err := ReadTranscript(context.Background(), claudereader.Adapter{}, "/nonexistent/transcript.jsonl")
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), "no error"))
}

// TestReadTranscript_HonorsTheAdapterItIsGiven reads a CODEX rollout — a
// format claude's adapter cannot parse — and proves the caller's engine
// choice is what decides the reader.
//
// This is the assertion that makes next-step capture cross-engine rather than
// claude-only: the hook is installed on every hooking backend, so a reader
// nailed to one vendor fires every turn on the others and yields nothing.
//
// MUTATION — ignore the adapter parameter and convert through
// claudereader.Adapter{} instead — turns this red (zero events, no text)
// while every claude-fixture test in this package stays green, which is
// exactly the blind spot a claude-only suite had.
func TestReadTranscript_HonorsTheAdapterItIsGiven(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "rollout-codex.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-27T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"what next"}]}}`,
		`{"timestamp":"2026-08-27T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Next I will land the codex adapter."}]}}`,
	}
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	evs, err := ReadTranscript(context.Background(), codexreader.Adapter{}, p)
	require.NoError(t, err)
	require.NotEmpty(t, evs, "the codex rollout must yield events through codex's own adapter")

	assert.Equal(t, "Next I will land the codex adapter.", LastAssistantText(evs))
}

package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// runTurnChangedCmd drives the hook exactly as the shell does — payload on
// stdin, one word on stdout — and returns that word plus anything warned.
func runTurnChangedCmd(t *testing.T, payload string) (word, warned string) {
	t.Helper()
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	var out bytes.Buffer
	cmd := &cobra.Command{RunE: runTurnChanged}
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(&out)
	cmd.SetErr(&sink)
	cmd.SetArgs(nil)
	require.NoError(t, cmd.Execute(), "a Stop hook must never fail the host turn")
	return strings.TrimSpace(out.String()), sink.String()
}

func writeClaudeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return p
}

func assistantTool(uuid, msgID, tool, input string) string {
	return `{"type":"assistant","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"tool_use","id":"t-` + uuid + `","name":"` + tool + `","input":` + input +
		`}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:01.000Z"}`
}

func userPrompt(text, uuid string) string {
	return `{"type":"user","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:00.000Z"}`
}

// TestTurnChanged_CoordinatorTurn is the defect. Every edit this turn caused
// happened inside a dispatched agent, in another worktree; this checkout is
// clean and the old git-status guard stayed silent. The word on stdout must
// be "changed".
func TestTurnChanged_CoordinatorTurn(t *testing.T) {
	p := writeClaudeTranscript(t,
		userPrompt("work the release blockers", "u1"),
		assistantTool("a1", "msg_1", "Read", `{"file_path":"/plan.md"}`),
		assistantTool("a2", "msg_2", "Agent", `{"prompt":"implement it in a worktree"}`),
	)
	word, _ := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "changed", word)
}

// TestTurnChanged_ConversationalTurn keeps the guard a guard: a turn that only
// read and answered must stay silent, or the checklist appears on every turn
// and is learned to be scrolled past.
func TestTurnChanged_ConversationalTurn(t *testing.T) {
	p := writeClaudeTranscript(t,
		userPrompt("what does the trust model do", "u1"),
		assistantTool("a1", "msg_1", "Read", `{"file_path":"/docs/trust.md"}`),
		assistantTool("a2", "msg_2", "Bash", `{"command":"git log -1 --oneline"}`),
	)
	word, warned := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "unchanged", word)
	assert.Empty(t, warned, "a clean read-only verdict warns about nothing")
}

func TestTurnChanged_EditTurn(t *testing.T) {
	p := writeClaudeTranscript(t,
		userPrompt("fix the typo", "u1"),
		assistantTool("a1", "msg_1", "Edit", `{"file_path":"/x/y.go"}`),
	)
	word, _ := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "changed", word)
}

// TestTurnChanged_UnreadableTranscript pins the fail-safe direction. Silence
// is the bug being fixed, so anything this command cannot measure is
// "changed" — and it says why, rather than vanishing.
func TestTurnChanged_UnreadableTranscript(t *testing.T) {
	cases := map[string]string{
		"missing_file":    `{"transcript_path":"/nonexistent/nope.jsonl"}`,
		"no_path_at_all":  `{}`,
		"unparsable_json": `not json at all`,
		"empty_payload":   ``,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			word, warned := runTurnChangedCmd(t, payload)
			assert.Equal(t, "changed", word, "an unmeasurable turn must still fire the contract")
			assert.NotEmpty(t, warned, "the reason for the fallback must reach stderr")
		})
	}
}

// TestTurnChanged_GarbageTranscript: a file this build cannot parse is a
// format break, not an unchanged turn.
func TestTurnChanged_GarbageTranscript(t *testing.T) {
	p := writeClaudeTranscript(t, "this is not json", "nor is this")
	word, warned := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "changed", word)
	assert.NotEmpty(t, warned)
}

// TestTurnChanged_EmptyTranscript: an empty file parses cleanly and records
// nothing, so there is nothing to close out and nothing to warn about.
func TestTurnChanged_EmptyTranscript(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty.jsonl")
	require.NoError(t, os.WriteFile(p, nil, 0o644))
	word, warned := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "unchanged", word)
	assert.Empty(t, warned)
}

// TestTurnChanged_PriorTurnEditDoesNotLeak: the turn is the unit. An edit made
// before the current prompt belongs to a turn that already closed out.
func TestTurnChanged_PriorTurnEditDoesNotLeak(t *testing.T) {
	p := writeClaudeTranscript(t,
		userPrompt("fix it", "u1"),
		assistantTool("a1", "msg_1", "Write", `{"file_path":"/x/y.go"}`),
		userPrompt("now explain what you did", "u2"),
		assistantTool("a2", "msg_2", "Read", `{"file_path":"/x/y.go"}`),
	)
	word, _ := runTurnChangedCmd(t, `{"transcript_path":"`+p+`"}`)
	assert.Equal(t, "unchanged", word)
}

// TestTurnChanged_IsRegisteredHidden keeps the command on the hook wire the
// generated harness files call, and out of the user-facing help.
func TestTurnChanged_IsRegisteredHidden(t *testing.T) {
	var found *cobra.Command
	for _, c := range hookCmd.Commands() {
		if c.Name() == "turn-changed" {
			found = c
		}
	}
	require.NotNil(t, found, "turn-changed must be registered under the hook group")
	assert.True(t, found.Hidden)
}

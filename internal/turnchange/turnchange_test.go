package turnchange

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// userEntry builds a main-thread user prompt — the marker CurrentTurn scopes
// back to.
func userEntry(text string) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeUser, Content: text}}
}

// toolUse builds one tool_use entry, optionally on a subagent sidechain.
func toolUse(name, input string, sidechain bool) agent.ChatEvent {
	return agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:      agent.EntryTypeToolUse,
		ToolName:  name,
		ToolInput: json.RawMessage(input),
		Sidechain: sidechain,
	}}
}

// TestClassifyEvents_ConversationalTurn_NoChange pins the guard's whole
// reason for existing: a turn that only answered a question must stay silent,
// or the checklist appears unconditionally and is learned to be scrolled past.
func TestClassifyEvents_ConversationalTurn_NoChange(t *testing.T) {
	d := ClassifyEvents([]agent.ChatEvent{
		userEntry("what does the trust model do?"),
		toolUse("Read", `{"file_path":"/docs/trust-model.md"}`, false),
		toolUse("Grep", `{"pattern":"trust"}`, false),
		toolUse("Bash", `{"command":"git log -1 --oneline"}`, false),
		{Entry: &agent.SessionEntry{Type: agent.EntryTypeAssistant, Content: "It has three states."}},
	})
	assert.False(t, d.Changed, "a read-only turn must not fire the contract")
}

// TestClassifyEvents_FileEdit_Changed is the ordinary case.
func TestClassifyEvents_FileEdit_Changed(t *testing.T) {
	d := ClassifyEvents([]agent.ChatEvent{
		userEntry("fix the typo"),
		toolUse("Edit", `{"file_path":"/x/y.go"}`, false),
	})
	assert.True(t, d.Changed)
	assert.Contains(t, d.Reason, "Edit")
}

// TestClassifyEvents_SubagentEditInAnotherWorktree_Changed is the defect this
// package was built for. The coordinator's own checkout stays clean because
// every edit happened inside a subagent, in a different worktree; the turn
// still changed a great deal and must be told so.
func TestClassifyEvents_SubagentEditInAnotherWorktree_Changed(t *testing.T) {
	d := ClassifyEvents([]agent.ChatEvent{
		userEntry("dispatch the release blockers"),
		toolUse("Read", `{"file_path":"/plan.md"}`, false),
		// The subagent's own tool calls ride the sidechain, and its edits
		// land in /home/babbitt/workspace/worktrees/other, not here.
		toolUse("Edit", `{"file_path":"/home/babbitt/workspace/worktrees/other/a.go"}`, true),
	})
	assert.True(t, d.Changed, "a sidechain edit in another worktree is still this turn's change")
}

// TestClassifyEvents_DispatchIsAChange covers the shape that made the
// tree-dirtiness proxy fail hardest: a coordinator whose ONLY change-making
// act is handing work to a child session whose edits never appear in this
// transcript at all.
func TestClassifyEvents_DispatchIsAChange(t *testing.T) {
	for _, tool := range []string{"Task", "Agent", "mcp__ctxloom__agent_run"} {
		t.Run(tool, func(t *testing.T) {
			d := ClassifyEvents([]agent.ChatEvent{
				userEntry("work the queue"),
				toolUse(tool, `{"prompt":"implement it"}`, false),
			})
			assert.True(t, d.Changed, "dispatching work to another agent changes things elsewhere")
		})
	}
}

// TestCurrentTurn_ScopesToLastMainThreadUserPrompt pins the scoping primitive:
// an edit made in a PREVIOUS turn must not keep firing the contract on every
// later conversational turn.
func TestCurrentTurn_ScopesToLastMainThreadUserPrompt(t *testing.T) {
	evs := []agent.ChatEvent{
		userEntry("edit the file"),
		toolUse("Write", `{"file_path":"/x"}`, false),
		userEntry("thanks, now explain it"),
		toolUse("Read", `{"file_path":"/x"}`, false),
	}
	scoped := CurrentTurn(evs)
	require.NotEmpty(t, scoped)
	assert.False(t, ClassifyEvents(evs).Changed, "a previous turn's edit is not this turn's change")

	for _, ev := range scoped {
		if ev.Entry != nil && ev.Entry.ToolName == "Write" {
			t.Fatal("CurrentTurn leaked a tool call from the previous turn")
		}
	}
}

// TestCurrentTurn_SidechainUserPromptDoesNotResetScope: a subagent's own
// prompt is written as a user entry too. Treating it as a turn boundary would
// discard every edit the subagent made before it — the exact evidence this
// command exists to find.
func TestCurrentTurn_SidechainUserPromptDoesNotResetScope(t *testing.T) {
	sidechainPrompt := userEntry("go edit that file")
	sidechainPrompt.Entry.Sidechain = true

	evs := []agent.ChatEvent{
		userEntry("dispatch it"),
		toolUse("Edit", `{"file_path":"/a"}`, true),
		sidechainPrompt,
		toolUse("Read", `{"file_path":"/b"}`, true),
	}
	assert.True(t, ClassifyEvents(evs).Changed)
}

// TestCurrentTurn_NoUserPrompt_ScopesWholeStream keeps the fail-safe polarity:
// with no boundary to scope to, everything counts rather than nothing.
func TestCurrentTurn_NoUserPrompt_ScopesWholeStream(t *testing.T) {
	evs := []agent.ChatEvent{toolUse("Write", `{"file_path":"/x"}`, false)}
	assert.Len(t, CurrentTurn(evs), 1)
	assert.True(t, ClassifyEvents(evs).Changed)
}

// TestClassifyEvents_Empty is the empty-transcript shape: nothing recorded,
// nothing to close out.
func TestClassifyEvents_Empty(t *testing.T) {
	assert.False(t, ClassifyEvents(nil).Changed)
}

func TestToolChanges(t *testing.T) {
	cases := []struct {
		name, tool, input string
		want              bool
	}{
		{"edit", "Edit", `{"file_path":"/x"}`, true},
		{"write", "Write", `{"file_path":"/x"}`, true},
		{"multiedit", "MultiEdit", `{"file_path":"/x"}`, true},
		{"notebookedit", "NotebookEdit", `{"notebook_path":"/x"}`, true},
		{"read", "Read", `{"file_path":"/x"}`, false},
		{"grep", "Grep", `{"pattern":"x"}`, false},
		{"glob", "Glob", `{"pattern":"x"}`, false},
		{"websearch", "WebSearch", `{"query":"x"}`, false},
		{"tasklist", "mcp__taskloom__task_list", `{}`, false},
		{"bash_read", "Bash", `{"command":"cat go.mod"}`, false},
		{"bash_write", "Bash", `{"command":"git commit -m x"}`, true},
		{"bash_unparsable_input", "Bash", `not json`, true},
		{"bash_no_command", "Bash", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := ToolChanges(tc.tool, json.RawMessage(tc.input))
			assert.Equal(t, tc.want, got)
			if tc.want {
				assert.NotEmpty(t, reason, "a changed verdict must name its evidence")
			}
		})
	}
}

// TestBashCommandChanges: the shell is the escape hatch every other tool can
// be spelled through, so its polarity is inverted from the tool namespace —
// only a command proven read-only is read-only.
func TestBashCommandChanges(t *testing.T) {
	readOnly := []string{
		"",
		"git status --porcelain",
		"git log -1 --oneline",
		"git diff HEAD",
		"cat scripts/hooks/verify-and-track.sh",
		"ls -la /tmp",
		"grep -rn 'foo' internal/ | head -20",
		"rg --files-with-matches turnchange",
		"jq -r '.stop_hook_active' < /tmp/x",
		"find . -name '*.go'",
		"sed -n '1,40p' main.go",
		"wc -l internal/turnchange/*.go",
		"git rev-parse --show-toplevel 2>/dev/null",
		"ps aux | grep ctxloom",
		"cd /repo && git status",
	}
	for _, c := range readOnly {
		assert.False(t, BashCommandChanges(c), "expected read-only: %q", c)
	}

	mutating := []string{
		"git commit -m 'fix'",
		"git add -A",
		"git checkout -b task/x",
		"echo hi > /tmp/f",
		"cat <<'EOF' > /tmp/f\nbody\nEOF",
		"printf x >> notes.txt",
		"rm -rf /tmp/scratch",
		"mv a b",
		"mkdir -p out",
		"sed -i 's/a/b/' file.go",
		"just build",
		"go build ./...",
		"grep -l foo . | xargs rm",
		"find . -name '*.tmp' -delete",
		"tee /tmp/out",
		"git status && git commit -m x",
		"MSG=$(rm -rf /tmp/x)",
	}
	for _, c := range mutating {
		assert.True(t, BashCommandChanges(c), "expected mutating: %q", c)
	}
}

// --- transcript-level (vendor reader) -------------------------------------

func assistantToolLine(uuid, msgID, tool, input string, sidechain bool) string {
	return `{"type":"assistant","isSidechain":` + boolStr(sidechain) + `,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"tool_use","id":"t-` + uuid + `","name":"` + tool + `","input":` + input +
		`}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:01.000Z"}`
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func promptLine(text, uuid string) string {
	return `{"type":"user","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:00.000Z"}`
}

func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "transcript.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// TestClassifyClaudeTranscript_CoordinatorTurn is the end-to-end coordinator
// case read through the real vendor adapter: the turn's only change-making
// act is dispatching a subagent, and the checkout it ran in is clean.
func TestClassifyClaudeTranscript_CoordinatorTurn(t *testing.T) {
	p := writeTranscript(t,
		promptLine("work the release blockers", "u1"),
		assistantToolLine("a1", "msg_1", "Read", `{"file_path":"/plan.md"}`, false),
		assistantToolLine("a2", "msg_2", "Agent", `{"prompt":"go implement it"}`, false),
	)
	d, err := ClassifyClaudeTranscript(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, d.Changed, "a dispatching coordinator turn must receive the close-out contract")
}

func TestClassifyClaudeTranscript_ConversationalTurn(t *testing.T) {
	p := writeTranscript(t,
		promptLine("what is the trust model", "u1"),
		assistantToolLine("a1", "msg_1", "Read", `{"file_path":"/docs/trust.md"}`, false),
		assistantToolLine("a2", "msg_2", "Bash", `{"command":"git log -1"}`, false),
	)
	d, err := ClassifyClaudeTranscript(context.Background(), p)
	require.NoError(t, err)
	assert.False(t, d.Changed)
}

func TestClassifyClaudeTranscript_EditTurn(t *testing.T) {
	p := writeTranscript(t,
		promptLine("fix it", "u1"),
		assistantToolLine("a1", "msg_1", "Edit", `{"file_path":"/x/y.go"}`, false),
	)
	d, err := ClassifyClaudeTranscript(context.Background(), p)
	require.NoError(t, err)
	assert.True(t, d.Changed)
}

// TestClassifyClaudeTranscript_PriorTurnEditDoesNotLeak proves the scoping is
// real end to end, not just in the pure classifier.
func TestClassifyClaudeTranscript_PriorTurnEditDoesNotLeak(t *testing.T) {
	p := writeTranscript(t,
		promptLine("fix it", "u1"),
		assistantToolLine("a1", "msg_1", "Edit", `{"file_path":"/x/y.go"}`, false),
		promptLine("now explain what you did", "u2"),
		assistantToolLine("a2", "msg_2", "Read", `{"file_path":"/x/y.go"}`, false),
	)
	d, err := ClassifyClaudeTranscript(context.Background(), p)
	require.NoError(t, err)
	assert.False(t, d.Changed)
}

// TestClassifyClaudeTranscript_Empty: a transcript with no lines parses
// cleanly and records nothing, so there is nothing to close out.
func TestClassifyClaudeTranscript_Empty(t *testing.T) {
	p := writeTranscript(t)
	d, err := ClassifyClaudeTranscript(context.Background(), p)
	require.NoError(t, err)
	assert.False(t, d.Changed)
}

// TestClassifyClaudeTranscript_Missing / _Unparsable: both must surface an
// error so the caller can fail SAFE IN THE SPEAKING DIRECTION.
func TestClassifyClaudeTranscript_Missing(t *testing.T) {
	_, err := ClassifyClaudeTranscript(context.Background(), filepath.Join(t.TempDir(), "nope.jsonl"))
	assert.Error(t, err)
}

func TestClassifyClaudeTranscript_Unparsable(t *testing.T) {
	p := writeTranscript(t, "this is not json", "neither is this")
	_, err := ClassifyClaudeTranscript(context.Background(), p)
	assert.Error(t, err, "a file this build cannot read must not be reported as an unchanged turn")
}

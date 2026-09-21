package mock

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// runTurn drives one scripted turn and decodes what the driver relayed.
func runTurn(t *testing.T, ex engine.Exec, prompt string) (engine.TurnResult, []agent.ChatEvent, error) {
	t.Helper()
	inst, err := New().Instance(engine.Session{Mode: engine.Structured, WorkDir: t.TempDir()})
	require.NoError(t, err)
	out := make(chan engine.Event, 64)
	res, terr := inst.Drivers()[0].Turn(context.Background(), ex, engine.Turn{Prompt: prompt}, out)
	close(out)
	var events []agent.ChatEvent
	for ev := range out {
		var ce agent.ChatEvent
		require.NoError(t, json.Unmarshal(ev.Payload, &ce))
		events = append(events, ce)
	}
	return res, events, terr
}

// TestTurn_EchoesThePromptAsTheAssistantsAnswer: the default turn is an
// ECHO — one assistant entry "mock chat: <text>" and a completion — relayed
// as chat events, with the same text as the turn's answer and the mock's one
// native key the next turn resumes by. The echoed text proves exactly what
// was delivered to the engine (context lead blocks included).
func TestTurn_EchoesThePromptAsTheAssistantsAnswer(t *testing.T) {
	res, events, err := runTurn(t, engine.Exec{Binary: "mock"}, "CTX\n\ndo the thing")
	require.NoError(t, err)
	assert.Equal(t, "mock chat: CTX\n\ndo the thing", res.Answer)
	assert.NotEmpty(t, res.NativeKey)
	var assistant, complete int
	for _, ev := range events {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			assistant++
			assert.Equal(t, res.Answer, ev.Entry.Content)
		case ev.Complete != nil:
			complete++
			assert.Equal(t, "end_turn", ev.Complete.StopReason)
		}
	}
	assert.Equal(t, 1, assistant, "one assistant entry")
	assert.Equal(t, 1, complete, "one completion, after the entries")
}

// TestTurn_HonoursTheResponseAndFailureKnobs: CTXLOOM_MOCK_RESPONSE replaces
// the echo (an EMPTY override is a deliberate empty reply, not an absent
// one); CTXLOOM_MOCK_FAIL_PREFIX marks the reply without discarding the
// evidence; CTXLOOM_MOCK_EXIT_CODE is a process that failed — the turn errs.
func TestTurn_HonoursTheResponseAndFailureKnobs(t *testing.T) {
	res, _, err := runTurn(t, engine.Exec{Env: map[string]string{"CTXLOOM_MOCK_RESPONSE": "MOCK-REPLY"}}, "anything")
	require.NoError(t, err)
	assert.Equal(t, "MOCK-REPLY", res.Answer)

	res, _, err = runTurn(t, engine.Exec{Env: map[string]string{"CTXLOOM_MOCK_RESPONSE": ""}}, "anything")
	require.NoError(t, err)
	assert.Equal(t, "", res.Answer, "an override of \"\" is a request for an empty reply")

	res, _, err = runTurn(t, engine.Exec{Env: map[string]string{"CTXLOOM_MOCK_FAIL_PREFIX": "1"}}, "x")
	require.NoError(t, err)
	assert.Equal(t, FailPrefix+"\nmock chat: x", res.Answer)

	_, _, err = runTurn(t, engine.Exec{Env: map[string]string{"CTXLOOM_MOCK_EXIT_CODE": "3"}}, "x")
	require.Error(t, err, "a non-zero exit code is a process that failed")
	assert.Contains(t, err.Error(), "3")
}

// TestTurn_ToolsMarkerEmitsTheFullEntryVocabulary: a prompt carrying TOOLS
// relays thinking, tool_use, tool_result and assistant entries before
// completing — the variety a liveness check asserts on, which the echo
// alone cannot produce.
func TestTurn_ToolsMarkerEmitsTheFullEntryVocabulary(t *testing.T) {
	_, events, err := runTurn(t, engine.Exec{}, "TOOLS please")
	require.NoError(t, err)
	var kinds []agent.SessionEntryType
	for _, ev := range events {
		if ev.Entry != nil {
			kinds = append(kinds, ev.Entry.Type)
		}
	}
	assert.Equal(t, []agent.SessionEntryType{agent.EntryTypeThinking, agent.EntryTypeToolUse, agent.EntryTypeToolResult, agent.EntryTypeAssistant}, kinds)
}

// TestTurn_WritesTheRecordFromWhatWasDelivered: CTXLOOM_MOCK_RECORD_FILE
// gets the turn's evidence — the prompt as delivered, the deny list the
// delivered settings file carries, the skills the delivered skills dir
// holds, the working directory — read back off the FILES the runner
// delivered (the exec's --context names the root), never a Setup of its own.
func TestTurn_WritesTheRecordFromWhatWasDelivered(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ConfigDirName, "skills", "review"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ConfigDirName, "settings.json"), []byte(`{"denyTools":["WebFetch","Bash"]}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(root, ContextFileName), []byte("RULES"), 0o600))
	record := filepath.Join(t.TempDir(), "record.txt")
	ex := engine.Exec{
		Args:    []string{contextFlag, filepath.Join(root, ContextFileName)},
		Env:     map[string]string{"CTXLOOM_MOCK_RECORD_FILE": record},
		WorkDir: "/work/dir",
	}
	_, _, err := runTurn(t, ex, "FRAGMENT-BODY: seeded\n\nthe prompt")
	require.NoError(t, err)
	got, err := os.ReadFile(record)
	require.NoError(t, err)
	assert.Contains(t, string(got), "workdir=/work/dir")
	assert.Contains(t, string(got), "=== DenyTools ===\nWebFetch\nBash\n")
	assert.Contains(t, string(got), "=== Skills ===\nreview\n")
	assert.Contains(t, string(got), "=== Prompt ===\nFRAGMENT-BODY: seeded\n\nthe prompt\n")
	assert.Contains(t, string(got), "=== Context ===\nRULES\n")

	// A record that cannot be written fails the turn: exit 0 with no record
	// would let a later assertion read a stale file from a previous run.
	_, _, err = runTurn(t, engine.Exec{Env: map[string]string{"CTXLOOM_MOCK_RECORD_FILE": filepath.Join(t.TempDir(), "no-such-dir", "r")}}, "x")
	require.Error(t, err)
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/memory"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// assistantText is an assistant message carrying TEXT rather than a tool call —
// what a turn ends with, and the thing next-step capture reads. Its tool-call
// sibling is assistantTool, in hook_turn_changed_test.go.
func assistantText(uuid, msgID, text string) string {
	return `{"type":"assistant","isSidechain":false,"cwd":"/repo","sessionId":"s","version":"2.1.44","message":{"model":"m","id":"` + msgID +
		`","type":"message","role":"assistant","content":[{"type":"text","text":` + jsonString(text) +
		`}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}},"uuid":"` + uuid + `","timestamp":"2026-08-22T10:00:02.000Z"}`
}

func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// stopPayload is the JSON the engine writes to a TurnEnd hook's stdin.
func stopPayload(transcriptPath string) string {
	return `{"session_id":"s","hook_event_name":"Stop","transcript_path":` + jsonString(transcriptPath) + `}`
}

// nextStepCmd wires stdin to payload, the way the engine invokes the hook.
func nextStepCmd(payload string) *cobra.Command {
	c := &cobra.Command{}
	c.SetIn(bytes.NewBufferString(payload))
	c.SetOut(&bytes.Buffer{})
	c.SetErr(&bytes.Buffer{})
	c.SetContext(context.Background())
	return c
}

// turnEndingWith is a one-turn transcript whose final assistant message is text.
func turnEndingWith(t *testing.T, final string) string {
	t.Helper()
	return writeClaudeTranscript(t,
		userPrompt("go", "u1"),
		assistantTool("a1", "msg_1", "Read", `{"file_path":"/plan.md"}`),
		assistantText("a2", "msg_2", final),
	)
}

// TestCaptureNextStep_WritesTheTurnsFinalMessageUnderTheHarp is the end-to-end
// assertion, and it reads the FILE. A hook that exits 0 having written nothing
// is this project's characteristic bug; only the payload catches it.
//
// MUTATION — drop the memory.WriteNextStep call at the end of captureNextStep
// (return nil instead) — turns this red.
func TestCaptureNextStep_WritesTheTurnsFinalMessageUnderTheHarp(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedHookSession(t, "claude-code")
	const final = "Next I will run just lint and then merge."

	require.NoError(t, captureNextStep(nextStepCmd(stopPayload(turnEndingWith(t, final)))))

	got, ok := memory.ReadNextStep(harp)
	require.True(t, ok, "the capture must leave a next step on disk, not merely exit 0")
	assert.Equal(t, final, got)
}

// TestCaptureNextStep_TakesTheFinalMessageNotTheToolCall pins that the captured
// text is the agent's own closing statement rather than any earlier entry in
// the turn.
//
// MUTATION — have LastAssistantText walk the turn forwards — turns this red.
func TestCaptureNextStep_TakesTheFinalMessageNotTheToolCall(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedHookSession(t, "claude-code")
	transcript := writeClaudeTranscript(t,
		userPrompt("go", "u1"),
		assistantText("a1", "msg_1", "an earlier, superseded remark"),
		assistantTool("a2", "msg_2", "Bash", `{"command":"just build"}`),
		assistantText("a3", "msg_3", "THE closing statement"),
	)

	require.NoError(t, captureNextStep(nextStepCmd(stopPayload(transcript))))

	got, _ := memory.ReadNextStep(harp)
	assert.Equal(t, "THE closing statement", got)
}

// TestCaptureNextStep_OverwritesEachTurn pins the mechanism that makes TurnEnd
// work where session_end does not: the LAST turn's statement is what survives
// when the session ends, without anything having to detect the ending.
//
// MUTATION — make WriteNextStep skip the write when the file already exists —
// turns this red: the first turn's text would still be there.
func TestCaptureNextStep_OverwritesEachTurn(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedHookSession(t, "claude-code")

	for _, text := range []string{"turn one intends A", "turn two intends B", "turn three intends C"} {
		require.NoError(t, captureNextStep(nextStepCmd(stopPayload(turnEndingWith(t, text)))))
	}

	got, ok := memory.ReadNextStep(harp)
	require.True(t, ok)
	assert.Equal(t, "turn three intends C", got, "the last turn's statement is the one that survives")
}

// TestCaptureNextStep_NamesWhyItCapturedNothing pins that every non-capture has
// a STATED reason. The alternative is exit 0 with zero bytes written and
// nothing said about which of several reasons applied.
//
// MUTATION — return nil instead of an error from any one of captureNextStep's
// guards — turns the corresponding subtest red.
func TestCaptureNextStep_NamesWhyItCapturedNothing(t *testing.T) {
	tests := []struct {
		name     string
		withHarp bool
		payload  func(t *testing.T) string
	}{
		{"no harp in the environment", false, func(t *testing.T) string { return stopPayload(turnEndingWith(t, "x")) }},
		{"undecodable payload", true, func(t *testing.T) string { return "not json at all" }},
		{"payload carries no transcript_path", true, func(t *testing.T) string { return `{"session_id":"s"}` }},
		{"transcript cannot be read", true, func(t *testing.T) string { return stopPayload("/nonexistent/t.jsonl") }},
		{"turn produced no assistant message", true, func(t *testing.T) string {
			return stopPayload(writeClaudeTranscript(t, userPrompt("go", "u1")))
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testsupport.Isolate(t)
			harp := ""
			if tc.withHarp {
				harp = seedHookSession(t, "claude-code")
			} else {
				t.Setenv(agent.SessionHarpEnv, "")
			}

			err := captureNextStep(nextStepCmd(tc.payload(t)))

			require.Error(t, err, "a capture that did not happen must say why")
			assert.NotEmpty(t, err.Error())
			if harp != "" {
				_, ok := memory.ReadNextStep(harp)
				assert.False(t, ok, "nothing must be stored when the capture failed")
			}
		})
	}
}

// TestRunHookNextStep_NeverFailsTheTurnAndSaysWhy pins both halves of the
// failure contract: the command exits 0 whatever went wrong (a TurnEnd hook
// that errors can stall the turn it fires on), AND the reason reaches the
// diagnostic channel — otherwise this is exactly the silent no-op the project
// keeps paying for.
//
// MUTATION — drop the clidiag.Warn in runHookNextStep — turns this red on the
// sink assertion. MUTATION — return the error instead of nil — red on NoError.
func TestRunHookNextStep_NeverFailsTheTurnAndSaysWhy(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv(agent.SessionHarpEnv, "")
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	assert.NoError(t, runHookNextStep(nextStepCmd(stopPayload("/nonexistent/t.jsonl")), nil),
		"a failed capture must not fail the turn")
	assert.Contains(t, sink.String(), "no next step captured",
		"a capture that did not happen must be NAMED, not silent")
}

// TestCaptureNextStep_CapturesOnCodex is the cross-engine assertion. The
// TurnEnd hook is installed on every hooking backend, so a capture wired to
// one vendor's format fires every turn on the others and stores nothing —
// leaving task-aware distillation permanently disengaged there while
// reporting no fault a user would notice.
//
// It reads the FILE, not the exit status: the failure being pinned is
// precisely a hook that ran, reported nothing wrong, and wrote zero bytes.
//
// MUTATION — have operations.ResolveTurnTranscript select the reader for
// config.BackendClaudeCode instead of the session's own entry.Backend —
// turns this red while the claude fixtures above stay green.
func TestCaptureNextStep_CapturesOnCodex(t *testing.T) {
	testsupport.Isolate(t)
	harp := seedHookSession(t, "codex")
	const final = "Next I will re-run the codex adapter fixtures."

	p := filepath.Join(t.TempDir(), "rollout-2026-08-27.jsonl")
	lines := []string{
		`{"timestamp":"2026-08-27T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"what next"}]}}`,
		`{"timestamp":"2026-08-27T10:00:01Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":` + jsonString(final) + `}]}}`,
	}
	require.NoError(t, os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644))

	require.NoError(t, captureNextStep(nextStepCmd(stopPayload(p))))

	got, ok := memory.ReadNextStep(harp)
	require.True(t, ok, "a codex turn must leave a next step on disk, not merely exit 0")
	assert.Equal(t, final, got)
}

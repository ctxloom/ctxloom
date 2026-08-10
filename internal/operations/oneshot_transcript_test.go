package operations

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// TestRunResolvedAgent_OneshotCapture_WritesTwoEntryTranscript pins tough-cloud
// S6: a ONESHOT Execute run (runResolvedAgent's Backend.Execute tail, shared by
// RunOneshot/ensemble/the delegate.go oneshot fallback) has no ChatEvent stream
// for the structured tee (S2) to capture — so runResolvedAgent itself writes a
// two-entry canonical transcript (user prompt + assistant stdout) when the
// caller's ExtraEnv carries a harp (agent.SessionHarpEnv), exactly as the
// delegated-child structured path is keyed. Reads the written file back
// through transcript.ParseTranscriptFile and asserts on the REAL payload, not
// just entry count (memory "silent-no-op-failure-mode" / "Mutation gate
// truthfulness" test discipline).
func TestRunResolvedAgent_OneshotCapture_WritesTwoEntryTranscript(t *testing.T) {
	testsupport.Isolate(t)
	harp := "tough-s6-oneshot-harp"

	stub := &stubClient{out: "  the assistant's captured stdout  \n"}
	factory := func(string, string, int) (pb.Client, error) { return stub, nil }

	res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
		Task:     "the user's request prompt",
		Label:    "codex-fast",
		Backend:  "codex",
		ExtraEnv: map[string]string{agent.SessionHarpEnv: harp},
		Factory:  factory,
	})
	require.NoError(t, err)
	assert.Equal(t, "the assistant's captured stdout", res.Output)

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	sess, err := transcript.ParseTranscriptFile(path, harp)
	require.NoError(t, err)
	require.Len(t, sess.Entries, 2)

	assert.Equal(t, agent.EntryTypeUser, sess.Entries[0].Type)
	assert.Equal(t, "the user's request prompt", sess.Entries[0].Content)

	assert.Equal(t, agent.EntryTypeAssistant, sess.Entries[1].Type)
	assert.Equal(t, "the assistant's captured stdout", sess.Entries[1].Content)
}

// TestRunResolvedAgent_OneshotCapture_NoHarpWritesNothing pins the degrade
// path: a caller with no harp in ExtraEnv (RunOneshot's own direct callers,
// e.g. `acp run`, today) must not crash and must not write any transcript
// file — RecordOneshot's own empty-harp no-op, exercised end to end through
// runResolvedAgent.
func TestRunResolvedAgent_OneshotCapture_NoHarpWritesNothing(t *testing.T) {
	testsupport.Isolate(t)
	harp := "tough-s6-oneshot-no-harp"

	stub := &stubClient{out: "some output"}
	factory := func(string, string, int) (pb.Client, error) { return stub, nil }

	res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
		Task:    "a prompt",
		Label:   "codex-fast",
		Backend: "codex",
		Factory: factory,
		// ExtraEnv deliberately unset — no harp.
	})
	require.NoError(t, err)
	assert.Equal(t, "some output", res.Output)

	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "no transcript should be written when no harp is present")
}

package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// payloadWithResponseBytes builds a claude PostToolUse payload whose
// tool_response encodes to at least n bytes, so a test can sit either side of
// a threshold without hard-coding a magic blob.
func payloadWithResponseBytes(t *testing.T, n int) []byte {
	t.Helper()
	raw, err := json.Marshal(strings.Repeat("x", n))
	require.NoError(t, err)
	p, err := json.Marshal(map[string]any{
		"session_id":    "s1",
		"tool_name":     "Bash",
		"tool_response": json.RawMessage(raw),
	})
	require.NoError(t, err)
	return p
}

// postToolWithResponseBytes is that payload decoded by claude's codec.
func postToolWithResponseBytes(t *testing.T, n int) engine.HookEvent {
	t.Helper()
	ev, err := claudeCodec(t).Decode("post_tool", payloadWithResponseBytes(t, n))
	require.NoError(t, err)
	return ev
}

// TestToolReflectResponse_FiresOnlyAtOrAboveThreshold pins BOTH sides of the
// threshold in one test. Asserting only that a large result fires would be
// satisfied by a hook that fires unconditionally -- which is the expensive
// failure here, since it would inject on every tool call in the session.
func TestToolReflectResponse_FiresOnlyAtOrAboveThreshold(t *testing.T) {
	const threshold = 2048
	assert.True(t, toolReflectResponse(postToolWithResponseBytes(t, 100), threshold).Empty(), "silent below the threshold")
	assert.Equal(t, engine.HookResponse{Context: ToolReflectReminder},
		toolReflectResponse(postToolWithResponseBytes(t, threshold*2), threshold), "the reminder above it")
}

// TestToolReflectResponse_NonPositiveThresholdDisables pins the disable path:
// a threshold of zero or less must never inject, even on a huge result.
func TestToolReflectResponse_NonPositiveThresholdDisables(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		t.Run(fmt.Sprintf("threshold=%d", threshold), func(t *testing.T) {
			assert.True(t, toolReflectResponse(postToolWithResponseBytes(t, 100_000), threshold).Empty())
		})
	}
}

// reflectCmd is the verb fired by claude with payload on stdin.
func reflectCmd(t *testing.T, payload []byte, out *bytes.Buffer) *cobra.Command {
	cmd := firedBy(t, &cobra.Command{}, claude.EngineName)
	cmd.SetIn(bytes.NewReader(payload))
	cmd.SetOut(out)
	return cmd
}

// TestRunHookToolReflect_AnswersInClaudesEnvelope: above the threshold the
// verb answers with the reminder in the envelope claude routes on; below it,
// with the codec's empty answer — valid JSON carrying nothing.
func TestRunHookToolReflect_AnswersInClaudesEnvelope(t *testing.T) {
	prev := toolReflectMinBytes
	toolReflectMinBytes = 2048
	t.Cleanup(func() { toolReflectMinBytes = prev })

	var out bytes.Buffer
	require.NoError(t, runHookToolReflect(reflectCmd(t, payloadWithResponseBytes(t, 4096), &out), nil))
	a := decodeClaudeAnswer(t, out.Bytes())
	require.NotNil(t, a.HookSpecificOutput)
	assert.Equal(t, "PostToolUse", a.HookSpecificOutput.HookEventName)
	assert.Equal(t, ToolReflectReminder, a.HookSpecificOutput.AdditionalContext)

	out.Reset()
	require.NoError(t, runHookToolReflect(reflectCmd(t, payloadWithResponseBytes(t, 10), &out), nil))
	assert.JSONEq(t, `{}`, out.String())
}

// TestRunHookToolReflect_AnUndecodablePayloadIsNamedAndSilent: a payload the
// codec cannot read produces silence, not a reminder (a hook that fired on
// everything it failed to understand would be loudest where it knew least),
// and the failure is NAMED on the diagnostic channel rather than vanishing.
//
// MUTATION -- drop the clidiag.Warn on the Decode error -- turns this red.
func TestRunHookToolReflect_AnUndecodablePayloadIsNamedAndSilent(t *testing.T) {
	var diag bytes.Buffer
	t.Cleanup(clidiag.SetSink(&diag))
	var out bytes.Buffer
	require.NoError(t, runHookToolReflect(reflectCmd(t, []byte("this is not json"), &out), nil), "a post_tool hook never fails the tool call")
	assert.JSONEq(t, `{}`, out.String())
	assert.Contains(t, diag.String(), claude.EngineName+" hook payload")
}

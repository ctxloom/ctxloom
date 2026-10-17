package llmturn

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// The turn carries the instruction ahead of the material, separated by one
// blank line, and the answer comes back trimmed.
func TestRunPrompt_InstructionAheadOfPayload(t *testing.T) {
	var sent string
	out, err := RunPrompt(context.Background(), func(_ context.Context, p string) (string, error) {
		sent = p
		return "  answer \n", nil
	}, "INSTRUCTION", "<fragment>body</fragment>")
	require.NoError(t, err)
	require.Equal(t, "answer", out)
	require.Equal(t, "INSTRUCTION\n\n<fragment>body</fragment>", sent)
}

// A turn that succeeds with nothing to say is a failed call, not an empty
// answer; a turn error and a missing runner surface as errors too.
func TestRunPrompt_Failures(t *testing.T) {
	_, err := RunPrompt(context.Background(), func(context.Context, string) (string, error) { return " \n", nil }, "i", "p")
	require.ErrorContains(t, err, "no output")

	boom := errors.New("boom")
	_, err = RunPrompt(context.Background(), func(context.Context, string) (string, error) { return "", boom }, "i", "p")
	require.ErrorIs(t, err, boom)

	_, err = RunPrompt(context.Background(), nil, "i", "p")
	require.ErrorContains(t, err, "no runner")
}

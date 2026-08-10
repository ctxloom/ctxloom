package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `--one-shot` is a one-shot. It gets exactly one turn, and the prompt
// is that turn — so every way of ending up without one has to be loud. Two
// were silent: an unreadable pipe (the io.ReadAll error was dropped on the
// floor) and an empty prompt after every source was exhausted, which launched
// a headless engine that asked nothing and exited 0.

// failingReader always fails with assert.AnError — the same broken-pipe
// shape, and identifiable, so tests that inject it can prove the CAUSE was
// propagated rather than merely that something went wrong. Shared with
// trust_interactive_test.go's withFailingStdin.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, assert.AnError }

func TestFinalizeRunPrompt_UnreadableStdinIsAnError(t *testing.T) {
	_, err := finalizeRunPrompt("", true, true, failingReader{})

	require.Error(t, err, "a --one-shot run whose only prompt source could not be read must not proceed")
	assert.ErrorIs(t, err, assert.AnError, "the underlying read failure must be reported, not swallowed")
}

func TestFinalizeRunPrompt_EmptyPrintPromptIsAnError(t *testing.T) {
	for name, stdin := range map[string]string{
		"empty pipe":      "",
		"whitespace only": "   \n\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := finalizeRunPrompt("", true, true, strings.NewReader(stdin))

			require.Error(t, err, "a --one-shot run with nothing to ask must not launch an engine")
			assert.Contains(t, err.Error(), "nothing to run")
		})
	}

	// No pipe at all, and no earlier source supplied one.
	_, err := finalizeRunPrompt("", true, false, strings.NewReader(""))
	require.Error(t, err)
}

func TestFinalizeRunPrompt_KeepsWorkingCases(t *testing.T) {
	// The universal-reducer shape: the pipe IS the prompt.
	got, err := finalizeRunPrompt("", true, true, strings.NewReader("  synthesize this  \n"))
	require.NoError(t, err)
	assert.Equal(t, "synthesize this", got)

	// An earlier source already won; stdin is not consulted.
	got, err = finalizeRunPrompt("from --prompt", true, true, failingReader{})
	require.NoError(t, err)
	assert.Equal(t, "from --prompt", got)

	// INTERACTIVE with no prompt is legitimate — it opens a session. This is
	// the discriminator that keeps the refusal above from being "reject empty
	// input everywhere".
	got, err = finalizeRunPrompt("", false, false, strings.NewReader(""))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestRecordOneshotAnswer_EmptyAnswerFails pins a fix: the go-plugin --one-shot
// arm handed an empty capture straight to transcript.RecordOneshot, whose
// contract is that "nothing to record" is a legitimate no-op — so a run that
// answered nothing wrote nothing, said nothing and exited 0.
func TestRecordOneshotAnswer_EmptyAnswerFails(t *testing.T) {
	for name, answer := range map[string]string{
		"zero bytes":      "",
		"whitespace only": "\n  \t\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := recordOneshotAnswer("some-harp", "claude-code", "do the thing", answer)

			require.Error(t, err, "a one-shot that produced no answer must not exit 0")
			var exitErr *ExitError
			require.True(t, errors.As(err, &exitErr), "the failure must carry a nonzero exit code, got %T", err)
			assert.NotZero(t, exitErr.Code)
		})
	}
}

// TestRecordOneshotAnswer_CaptureFailureDoesNotFailTheRun is the discriminator
// in the other direction: a run that DID answer keeps its exit code even when
// the transcript cannot be written. Here the harp is empty, which RecordOneshot
// reports as an error precisely because there is something to record and
// nowhere to file it — worth a warning, never worth failing an answered run.
func TestRecordOneshotAnswer_CaptureFailureDoesNotFailTheRun(t *testing.T) {
	err := recordOneshotAnswer("", "claude-code", "do the thing", "here is the answer")

	assert.NoError(t, err, "losing transcript capture must not change the exit code of a run that answered")
}

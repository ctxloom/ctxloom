package agent

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U080-F01: RunOneshotTurn is the shared drain/render plumbing opencode's and
// acp's Execute both used to carry duplicated (byte-for-byte, per reprise) —
// including the same defect: an empty prompt was sent to the engine with no
// check at all, and a turn producing zero assistant text exited 0 with zero
// bytes written and no diagnostic, the project's characteristic silent-noop
// shape.

func TestRunOneshotTurn_EmptyPromptIsRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	called := false
	_, err := RunOneshotTurn(&Fragment{Content: "   "}, &ModelInfo{}, 0, &stdout, &stderr,
		func(in <-chan ChatMessage, out chan<- ChatEvent) error {
			called = true
			close(out)
			return nil
		})
	require.Error(t, err, "an empty (or whitespace-only) prompt must be refused before ever reaching the engine")
	assert.False(t, called, "the engine must never be invoked for an empty prompt")
}

func TestRunOneshotTurn_NoAssistantTextIsWarned(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res, err := RunOneshotTurn(&Fragment{Content: "hi"}, &ModelInfo{ModelName: "m"}, 0, &stdout, &stderr,
		func(in <-chan ChatMessage, out chan<- ChatEvent) error {
			go func() {
				for range in {
				}
			}()
			// A turn that emits nothing at all (or only non-assistant entries) —
			// the tool-only-turn / silently-broken-turn shape.
			close(out)
			return nil
		})
	require.NoError(t, err, "a textless turn is a degraded outcome, not a hard failure")
	require.NotNil(t, res)
	assert.Equal(t, int32(0), res.ExitCode)
	assert.Empty(t, stdout.String(), "no assistant text means nothing legitimate to write to stdout")
	assert.NotEmpty(t, stderr.String(), "a textless turn must be warned, not silently reported as a normal success")
}

// TestRunOneshotTurn_NoAssistantTextNamesStopReason pins U011-F03's remaining
// half (the empty-prompt refusal above already closed the other): a textless
// turn's warning must name WHY, from the turn's own Complete.StopReason —
// distinguishing a refusal/cutoff/cancellation from an ordinary tool-only
// turn — rather than emitting the identical message for all of them.
func TestRunOneshotTurn_NoAssistantTextNamesStopReason(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := RunOneshotTurn(&Fragment{Content: "hi"}, &ModelInfo{}, 0, &stdout, &stderr,
		func(in <-chan ChatMessage, out chan<- ChatEvent) error {
			go func() {
				for range in {
				}
			}()
			out <- ChatEvent{Complete: &TurnMeta{StopReason: "refusal"}}
			close(out)
			return nil
		})
	require.NoError(t, err)
	assert.Contains(t, stderr.String(), "refusal", "the warning must name the turn's actual stop reason")
}

func TestRunOneshotTurn_WritesAssistantText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	res, err := RunOneshotTurn(&Fragment{Content: "hi"}, &ModelInfo{}, 0, &stdout, &stderr,
		func(in <-chan ChatMessage, out chan<- ChatEvent) error {
			go func() {
				for range in {
				}
			}()
			out <- ChatEvent{Entry: &SessionEntry{Type: EntryTypeAssistant, Content: "hello back"}}
			close(out)
			return nil
		})
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Contains(t, stdout.String(), "hello back")
	assert.Empty(t, stderr.String(), "a turn that produced real text must not also warn")
}

func TestRunOneshotTurn_PropagatesChatError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	_, err := RunOneshotTurn(&Fragment{Content: "hi"}, &ModelInfo{}, 0, &stdout, &stderr,
		func(in <-chan ChatMessage, out chan<- ChatEvent) error {
			go func() {
				for range in {
				}
			}()
			close(out)
			return assert.AnError
		})
	require.Error(t, err)
}

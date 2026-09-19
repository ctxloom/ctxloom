package memory

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// preTaskHintPrompt reproduces distillPrompt's ENTIRE pre-task-hint expression,
// independently of the function under test. Keeping a second copy here is
// deliberate and is the whole point: it is the only way to assert that adding
// the TaskHint field left the no-hint prompt untouched. If distillPrompt starts
// emitting anything else for an empty hint, these two disagree.
func preTaskHintPrompt(c *Compactor) string {
	return fmt.Sprintf("%s\n- The finished essence must be under %d characters.\n",
		sessionDistillPrompt, c.config.EssenceMaxChars)
}

// TestDistillPrompt_EmptyTaskHintIsByteIdentical is the inertness proof. A
// no-hint distill must be behaviourally free: every essence produced without a
// hint has to be attributable to the same prompt that produced essences before
// task-awareness existed.
//
// MUTATION — make distillPrompt append its "## Task hint" section
// unconditionally (drop the `if hint != ""` guard, or emit an empty section) —
// turns this red on the byte comparison.
func TestDistillPrompt_EmptyTaskHintIsByteIdentical(t *testing.T) {
	c := &Compactor{config: CompactionConfig{EssenceMaxChars: 7331}}
	require.Empty(t, c.config.TaskHint, "this test is vacuous unless the hint is genuinely unset")

	got, err := c.distillPrompt()
	require.NoError(t, err)

	want := preTaskHintPrompt(c)
	assert.Equal(t, want, got, "an unset task hint must not change one byte of the prompt")
	assert.Len(t, got, len(want), "byte length must match, not merely the text compare")
	assert.NotContains(t, got, "Task hint", "no hint means no hint section")
}

// TestDistillPrompt_WhitespaceOnlyTaskHintIsAlsoInert closes the gap a bare
// `!= ""` check would leave: a captured next step of nothing but blank lines
// must be treated as absent, not appended as an empty instruction.
//
// MUTATION — change distillPrompt's guard from strings.TrimSpace(...) != "" to
// c.config.TaskHint != "" — turns this red.
func TestDistillPrompt_WhitespaceOnlyTaskHintIsAlsoInert(t *testing.T) {
	c := &Compactor{config: CompactionConfig{EssenceMaxChars: 7331, TaskHint: "  \n\t "}}

	got, err := c.distillPrompt()
	require.NoError(t, err)

	assert.Equal(t, preTaskHintPrompt(c), got, "a whitespace-only hint is not a hint")
}

// TestDistillPrompt_TaskHintIsAppendedVerbatimAfterTheNoHintPrompt asserts the
// hint actually reaches the model, and that it EXTENDS the existing prompt
// rather than restructuring it — the required sections must survive.
//
// MUTATION — drop the `prompt += ...` append (or interpolate something other
// than the hint) — turns this red.
func TestDistillPrompt_TaskHintIsAppendedVerbatimAfterTheNoHintPrompt(t *testing.T) {
	const hint = "Merge feat/next-step-capture and close the taskloom entry."
	c := &Compactor{config: CompactionConfig{EssenceMaxChars: 7331, TaskHint: hint}}

	got, err := c.distillPrompt()
	require.NoError(t, err)

	base := preTaskHintPrompt(c)
	assert.True(t, strings.HasPrefix(got, base),
		"the hint must EXTEND the prompt; the required sections are not replaced by it")
	assert.Contains(t, got, hint, "the captured next step must reach the model verbatim")
	assert.Greater(t, len(got), len(base), "a present hint must add bytes")
}

// TestSessionDistillPrompt_TellsTheModelWhatToDoWithAHint pins that the
// instruction and the mechanism ship together. A hint appended to a prompt
// that never mentions hints is a string the model has been given no reason to
// act on, and the appending code cannot detect that on its own.
//
// MUTATION — delete the task-hint bullet from
// resources/prompts/session-distill.md — turns this red.
func TestSessionDistillPrompt_TellsTheModelWhatToDoWithAHint(t *testing.T) {
	assert.Contains(t, sessionDistillPrompt, "task hint",
		"the prompt must instruct the model on what a task hint is for")
	assert.Contains(t, sessionDistillPrompt, "never drop a required section",
		"retaining FOR the hint must not license dropping the required sections")
}

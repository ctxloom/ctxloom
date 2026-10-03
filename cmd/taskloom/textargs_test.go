package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
)

// Task text that begins with "-" cannot be told apart from a flag: pflag sees
// argv, not the shell's quoting. These pin the two halves of skinless-
// encounter: the end-of-flags marker carries such text through intact, and
// every way the unmarked text fails names that marker instead of only the
// flag the caller never meant.

func storedTexts(t *testing.T) []string {
	t.Helper()
	tc, err := taskContextSingle()
	require.NoError(t, err)
	res, err := operations.ListTasks(tc, operations.ListOptions{IncludeDone: true})
	require.NoError(t, err)
	out := make([]string, len(res.Tasks))
	for i, task := range res.Tasks {
		out[i] = task.Text
	}
	return out
}

func TestAdd_TextBeginningWithAFlagSurvivesTheEndOfFlagsMarker(t *testing.T) {
	taskstest.ProjectDir(t)
	_, err := executeTaskloom(t, "add", "--format", "text", "--", "--json emits the wrong envelope")
	require.NoError(t, err)
	assert.Equal(t, []string{"--json emits the wrong envelope"}, storedTexts(t))
}

func TestAdd_TextReadAsAFlagFailsNamingTheEndOfFlagsMarker(t *testing.T) {
	for name, text := range map[string]string{
		"exactly a real global bool flag":   "--json",
		"a real flag that takes a value":    "--format",
		"an unknown flag with a body after": "--nosuchflag whatever",
		"a shorthand-looking body":          "-x marks the spot",
	} {
		t.Run(name, func(t *testing.T) {
			taskstest.ProjectDir(t)
			_, err := executeTaskloom(t, "add", text)
			require.Error(t, err)
			assert.True(t, errors.Is(err, errTextReadAsFlag), "the failure must name the -- remedy, got: %v", err)
			assert.Empty(t, storedTexts(t), "nothing may be written")
		})
	}
}

func TestEdit_TextBeginningWithAFlagSurvivesTheEndOfFlagsMarker(t *testing.T) {
	taskstest.ProjectDir(t)
	added, err := operations.AddTask(mustTaskContext(t), "original", "", "")
	require.NoError(t, err)

	_, err = executeTaskloom(t, "edit", "--format", "text", added.Task.HarpID, "--", "--json emits the wrong envelope")
	require.NoError(t, err)
	assert.Equal(t, []string{"--json emits the wrong envelope"}, storedTexts(t))
}

func TestEdit_TextReadAsAFlagFailsNamingTheEndOfFlagsMarker(t *testing.T) {
	for name, text := range map[string]string{
		"exactly a real global bool flag":   "--json",
		"an unknown flag with a body after": "--nosuchflag whatever",
	} {
		t.Run(name, func(t *testing.T) {
			taskstest.ProjectDir(t)
			added, err := operations.AddTask(mustTaskContext(t), "original", "", "")
			require.NoError(t, err)

			_, err = executeTaskloom(t, "edit", added.Task.HarpID, text)
			require.Error(t, err)
			assert.True(t, errors.Is(err, errTextReadAsFlag), "the failure must name the -- remedy, got: %v", err)
			assert.Equal(t, []string{"original"}, storedTexts(t), "the text must be left as it was")
		})
	}
}

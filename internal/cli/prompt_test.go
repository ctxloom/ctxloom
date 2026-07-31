package cli

import (
	"bufio"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromptLine_SharedReaderKeepsTypeAheadAcrossPrompts pins the invariant
// that gives these primitives a home of their own: every interactive prompt in
// the CLI reads through ONE buffered reader. bufio reads ahead, so a fresh
// reader per prompt would take the user's second answer into a buffer that is
// then thrown away — back-to-back confirmations would lose pasted or typed-
// ahead input and then block on a terminal the user believes they have already
// answered (ctxloom-code-08-002).
//
// Two prompts, one write: the second answer is only reachable if both prompts
// read through the same reader.
func TestPromptLine_SharedReaderKeepsTypeAheadAcrossPrompts(t *testing.T) {
	saved := stdinReader
	stdinReader = bufio.NewReader(strings.NewReader("first\nsecond\n"))
	t.Cleanup(func() { stdinReader = saved })

	one, err := promptLine("a? ")
	require.NoError(t, err)
	assert.Equal(t, "first", one)

	two, err := promptLine("b? ")
	require.NoError(t, err)
	assert.Equal(t, "second", two,
		"the second prompt must see input the first one read ahead into the shared buffer")
}

func TestPromptYesNo_OnlyAnExplicitYesIsYes(t *testing.T) {
	for answer, want := range map[string]bool{
		"y": true, "Y": true, "yes": true, "YES": true, "  yes  ": true,
		"n": false, "": false, "yep": false, "sure": false,
	} {
		saved := stdinReader
		stdinReader = bufio.NewReader(strings.NewReader(answer + "\n"))
		got, err := promptYesNo("? ")
		stdinReader = saved

		require.NoError(t, err, answer)
		assert.Equal(t, want, got, "answer %q", answer)
	}
}

func TestPlural(t *testing.T) {
	assert.Equal(t, "y", plural(1, "y", "ies"))
	assert.Equal(t, "ies", plural(0, "y", "ies"))
	assert.Equal(t, "ies", plural(2, "y", "ies"))
}

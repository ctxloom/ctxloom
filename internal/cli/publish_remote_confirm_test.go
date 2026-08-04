package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The publish-remote prompt is the most consequential text on the publishing
// path: it is the one moment a human is asked where their signed content is
// about to go. It is asserted directly, because the no-TTY gate in
// publishRemoteAsk means no in-process test ever reaches it through that door
// (isInteractiveTerminal() is false in every test binary).

func TestPromptPublishRemote_ShowsTheDestinationAndTheConsequence(t *testing.T) {
	restore := stdinReader
	t.Cleanup(func() { stdinReader = restore })
	stdinReader = bufio.NewReader(strings.NewReader("y\n"))

	var out bytes.Buffer
	yes := promptPublishRemote(&out, "file:///srv/bundles.git")
	require.True(t, yes)

	shown := out.String()
	assert.Contains(t, shown, "file:///srv/bundles.git", "the prompt must name the destination")
	assert.Contains(t, shown, "signed content", "the prompt must say what is being sent")
	assert.Contains(t, shown, "typo", "the prompt must name the mistake it exists to catch")
	assert.Contains(t, shown, "not be asked about this remote again",
		"the prompt must say the answer is remembered, or people will expect it every time")
}

func TestPromptPublishRemote_AnythingButYesIsNo(t *testing.T) {
	restore := stdinReader
	t.Cleanup(func() { stdinReader = restore })

	for _, answer := range []string{"n\n", "\n", "maybe\n", "Y E S\n"} {
		stdinReader = bufio.NewReader(strings.NewReader(answer))
		var out bytes.Buffer
		assert.False(t, promptPublishRemote(&out, "file:///srv/bundles.git"),
			"answer %q must not authorise a publish", answer)
	}
}

// A closed stdin (EOF) is not an affirmative. This is the shape a piped or
// backgrounded invocation takes if it ever reaches the prompt at all.
func TestPromptPublishRemote_EOFIsNo(t *testing.T) {
	restore := stdinReader
	t.Cleanup(func() { stdinReader = restore })
	stdinReader = bufio.NewReader(strings.NewReader(""))

	var out bytes.Buffer
	assert.False(t, promptPublishRemote(&out, "file:///srv/bundles.git"))
}

// With no terminal, publishRemoteAsk must hand back NIL rather than a callback
// that answers no — nil is how package remote tells "nobody could be asked"
// from "you declined", and only the first message tells an agent or a CI job
// what to do about it.
func TestPublishRemoteAsk_IsNilWithoutATerminal(t *testing.T) {
	require.False(t, isInteractiveTerminal(), "the fixture assumes a test binary has no terminal")
	assert.Nil(t, publishRemoteAsk(&cobra.Command{}))
}

package cli

import (
	"bufio"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// discoverOneRepo is a MockFetcher whose search returns exactly one result, so
// the display block and the interactive add-loop below it are both reached.
func discoverOneRepo() *remote.MockFetcher {
	f := remote.NewMockFetcher()
	f.Repos = []remote.RepoInfo{{
		Owner: "alice", Name: "ctxloom", Description: "alice's context",
		Stars: 3, URL: "https://github.com/alice/ctxloom", Forge: remote.ForgeGitHub,
	}}
	return f
}

// TestPromptRemoteName_UnreadableInputIsNotAnAcceptedDefault pins a fix:
// `nameInput, _ := reader.ReadString('\n')` turned an EOF or read error into
// the empty string, which the very next line reads as "the user pressed Enter
// to accept the default" — and the caller then registers a remote under a name
// nobody supplied. Answering "" is consent to the default; failing to answer is
// not, and they must not produce the same value.
func TestPromptRemoteName_UnreadableInputIsNotAnAcceptedDefault(t *testing.T) {
	name, ok := promptRemoteName(bufio.NewReader(strings.NewReader("")), "alice")
	assert.False(t, ok, "input that ended before an answer is not an accepted default")
	assert.Empty(t, name)
}

// TestPromptRemoteName_AnswersAreStillAnswers is the discriminator for the pin
// above: an explicit empty line still means the default, a typed name still
// means itself, and a final line WITHOUT a trailing newline (which ReadString
// reports with io.EOF) is an answer too, not a read failure.
func TestPromptRemoteName_AnswersAreStillAnswers(t *testing.T) {
	for _, tc := range []struct {
		input, want string
	}{
		{"\n", "alice"},
		{"mine\n", "mine"},
		{"  mine  \n", "mine"},
		{"mine", "mine"},
	} {
		name, ok := promptRemoteName(bufio.NewReader(strings.NewReader(tc.input)), "alice")
		assert.True(t, ok, "input %q is an answer", tc.input)
		assert.Equal(t, tc.want, name, "input %q", tc.input)
	}
}

// TestInteractiveAdd_ReadsThroughTheSharedStdinReader pins the invariant
// run.go documents for interactive input: every prompt reads through
// the single package-level stdinReader, because a fresh bufio.Reader silently
// discards whatever a previous reader buffered past its line (type-ahead, or a
// pasted answer to two back-to-back confirmations). interactiveAdd opened its
// own reader over os.Stdin and so both lost those bytes and left its own
// consumed input invisible to later prompts. Asserting on what remains in the
// shared reader is what discriminates: a private reader leaves it untouched.
func TestInteractiveAdd_ReadsThroughTheSharedStdinReader(t *testing.T) {
	orig := stdinReader
	t.Cleanup(func() { stdinReader = orig })
	stdinReader = bufio.NewReader(strings.NewReader("q\nleftover\n"))

	cfg := config.NewFixture(config.Fixture{})
	repos := []operations.RepoEntry{{Owner: "alice", Name: "ctxloom", URL: "https://github.com/alice/ctxloom"}}
	captureStdout(t, func() {
		require.NoError(t, interactiveAdd(&cobra.Command{}, cfg, repos))
	})

	rest, err := stdinReader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "leftover\n", rest,
		"the 'q' must have been consumed FROM the shared reader, not from a private one")
}

// TestRunRemoteDiscover_NoInteractivePromptOffATTY pins a fix: the add-loop
// was entered unconditionally, so a piped or redirected `remote discover`
// wrote an interactive prompt nobody could answer into its own output and then
// quit on the resulting EOF. Every other interactive surface in this package
// gates on isInteractiveTerminal() first; this one must too. The test process
// is never a terminal, which is exactly the condition being pinned.
func TestRunRemoteDiscover_NoInteractivePromptOffATTY(t *testing.T) {
	var err error
	out := captureStdout(t, func() {
		err = runRemoteDiscover(&cobra.Command{}, nil, discoverTestConfig, discoverOneRepo())
	})
	require.NoError(t, err)
	assert.Contains(t, out, "alice/ctxloom", "the results themselves are still listed")
	assert.NotContains(t, out, "Add remote?",
		"a prompt nobody can answer must not be written into piped output")
}

// TestRunRemoteDiscover_ProgressLineIsClosedBeforeAnError pins a fix: the
// "Searching repositories..." progress line is deliberately newline-less so a
// result count can be appended to it, but the early error return left it
// dangling and the error text rendered as a suffix of the progress line
// ("Searching repositories...Error: ..."). The progress line must be
// terminated before the command hands an error back to the error printer.
func TestRunRemoteDiscover_ProgressLineIsClosedBeforeAnError(t *testing.T) {
	origSource := discoverSource
	t.Cleanup(func() { discoverSource = origSource })
	// An unsupported --source is the reachable path on which DiscoverRemotes
	// itself errors, before anything has closed the progress line.
	discoverSource = "gitlab"

	var err error
	out := captureStdout(t, func() {
		err = runRemoteDiscover(&cobra.Command{}, nil, discoverTestConfig, remote.NewMockFetcher())
	})
	require.Error(t, err)
	assert.Equal(t, "Searching repositories...\n", out,
		"the progress line must be terminated so the error does not read as its continuation")
}

func discoverTestConfig() (*config.Config, error) {
	return config.NewFixture(config.Fixture{}), nil
}

// TestRunRemoteDiscover_TotalSearchFailureIsAnErrorNotEmptyClaim is the
// missing regression test. discoverCmd's inline RunE used to print the exact
// same "No ctxloom repositories found." on a total search failure (every
// configured source erroring — today that is the sole GitHub fetcher) as it
// did on a genuinely empty, successful search, and returned nil either way.
// The fix (already landed) distinguishes the two; this pins it against a
// live caller instead of leaving the fix unguarded. Extracted into
// runRemoteDiscover (mirroring runRemoteUpgrade's injected-loadConfig shape)
// specifically so this is expressible without a real cobra dispatch or
// network access.
func TestRunRemoteDiscover_TotalSearchFailureIsAnErrorNotEmptyClaim(t *testing.T) {
	fetcher := remote.NewMockFetcher()
	fetcher.SearchReposErr = assert.AnError

	cmd := &cobra.Command{}
	err := runRemoteDiscover(cmd, nil, discoverTestConfig, fetcher)
	require.Error(t, err, "a total search failure must surface as an error, not a silent 'no repositories found' success")
	assert.Contains(t, err.Error(), "search failed")
}

// TestRunRemoteDiscover_GenuinelyEmptySearchSucceeds is the discriminator:
// zero results with NO fetcher error is a legitimately empty, successful
// search and must not be confused with the failure case above.
func TestRunRemoteDiscover_GenuinelyEmptySearchSucceeds(t *testing.T) {
	fetcher := remote.NewMockFetcher()
	fetcher.Repos = nil // zero results, no error

	cmd := &cobra.Command{}
	err := runRemoteDiscover(cmd, nil, discoverTestConfig, fetcher)
	assert.NoError(t, err, "a genuinely empty search is a success, not a failure")
}

package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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

// TestRunRemoteDiscover_NoInteractivePromptOffATTY pins U040-F19: the add-loop
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

// TestRunRemoteDiscover_ProgressLineIsClosedBeforeAnError pins U040-F22: the
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

// TestRunRemoteDiscover_TotalSearchFailureIsAnErrorNotEmptyClaim is U040-F01's
// missing regression test. discoverCmd's inline RunE used to print the exact
// same "No ctxloom repositories found." on a total search failure (every
// configured source erroring — today that is the sole GitHub fetcher) as it
// did on a genuinely empty, successful search, and returned nil either way.
// The fix (already landed) distinguishes the two; this pins it against a
// live caller instead of leaving the fix unguarded. Extracted into
// runRemoteDiscover (U040-F01 escalation, mirroring runRemoteUpgrade's
// injected-loadConfig shape) specifically so this is expressible without a
// real cobra dispatch or network access.
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

package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// bundleShowTrustReviewHeader is the first line offerBundleTrust writes; its
// presence on stderr is how a test observes that the interactive walk ran.
const bundleShowTrustReviewHeader = `Per-item effective trust for bundle "demo"`

// withInteractiveTerminal makes the TTY gate report a terminal for the test's
// duration, so the only thing standing between `bundle show -i` and the
// interactive walk is the format check under test.
func withInteractiveTerminal(t *testing.T) {
	t.Helper()
	saved := isInteractiveTerminal
	isInteractiveTerminal = func() bool { return true }
	t.Cleanup(func() { isInteractiveTerminal = saved })
}

// TestBundleShow_StructuredFormatsNeverTakeTheInteractiveWalk proves that a
// machine-readable invocation never blocks on a prompt: `bundle show -i`
// on a terminal offers the trust walk for the human (text) surface, and
// suppresses it for EVERY structured format — not only for json.
func TestBundleShow_StructuredFormatsNeverTakeTheInteractiveWalk(t *testing.T) {
	newIsolatedFlowProject(t)
	noAgentEnv(t)
	neutralizeRefresh(t)
	withInteractiveTerminal(t)
	withEmptyStdin(t)
	cfg, err := config.LoadFresh()
	require.NoError(t, err)
	_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
		Name: "demo",
		Fragments: map[string]operations.BundleFragmentInput{
			"x": {Content: "body", NoDistill: true},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { bundleShowInteractive = false })

	// Control: the human surface DOES walk, so an absent header below means
	// the walk was suppressed, not that this harness cannot see it.
	_, stderr, err := execRootCmdBoth(t, "bundle", "show", "demo", "-i", "--format", "text")
	require.NoError(t, err)
	require.Contains(t, stderr, bundleShowTrustReviewHeader, "control: text -i on a terminal offers the walk")

	for _, format := range []string{"json", "yaml", "toml"} {
		t.Run(format, func(t *testing.T) {
			stdout, stderr, err := execRootCmdBoth(t, "bundle", "show", "demo", "-i", "--format", format)
			require.NoError(t, err)
			assert.NotEmpty(t, stdout, "the structured view must still render")
			assert.NotContains(t, stderr, bundleShowTrustReviewHeader,
				"%s is machine-readable: it must never launch the interactive trust walk", format)
		})
	}
}

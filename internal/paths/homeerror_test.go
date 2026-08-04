package paths

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// withUnresolvableHome empties $HOME so os.UserHomeDir fails, which is the one
// fault every home-rooted resolver in this package bottoms out in.
//
// It returns the bare error text those resolvers used to hand back verbatim,
// and asserts the fault is real first: an isolation helper that leaves HOME
// resolvable would make every assertion below pass against fixed and unfixed
// code alike.
func withUnresolvableHome(t *testing.T) string {
	t.Helper()
	testsupport.Isolate(t)
	t.Setenv("HOME", "")

	_, err := os.UserHomeDir()
	require.Error(t, err, "the fixture did not make os.UserHomeDir fail; nothing below is exercised")
	return err.Error()
}

// TestHomeRootedResolvers_WrapTheHomeFailure pins the diagnostic contract for
// this package's home root.
//
// Fourteen exported resolvers hang off one os.UserHomeDir call, and each used
// to return its error verbatim. The result was that thirteen different
// questions — where is the session index, where is the trust root, where is
// this harp's transcript — all failed with the identical four-word string
// "$HOME is not defined", carrying nothing about what was being resolved. An
// error that cannot distinguish thirteen callers is a diagnostic only for
// whoever already knows which one ran.
//
// The assertion is deliberately not on exact wording: it requires that the
// underlying cause SURVIVES (so nothing is swallowed or reworded) and that the
// message is more than the cause alone (so something names the operation).
// That holds any future rewording to the same contract without pinning prose.
func TestHomeRootedResolvers_WrapTheHomeFailure(t *testing.T) {
	bare := withUnresolvableHome(t)

	noArg := map[string]func() (string, error){
		"HomeSessionsDir":             HomeSessionsDir,
		"SessionIndexPath":            SessionIndexPath,
		"HomeApprovalsPath":           HomeApprovalsPath,
		"HomePublishRemotesPath":      HomePublishRemotesPath,
		"HomeLegacyPublishRemotesDir": HomeLegacyPublishRemotesDir,
		"HomeAllowedSignersPath":      HomeAllowedSignersPath,
		"HomeDistrustedSignersPath":   HomeDistrustedSignersPath,
		"TriggerCacheDir":             TriggerCacheDir,
	}
	harpArg := map[string]func(string) (string, error){
		"HarpDir":                            HarpDir,
		"HarpEssencePath":                    HarpEssencePath,
		"HarpEphemeralDir":                   HarpEphemeralDir,
		"HarpPersistDir":                     HarpPersistDir,
		"HarpTranscriptStoreDir":             HarpTranscriptStoreDir,
		"HarpCanonicalTranscriptPath":        HarpCanonicalTranscriptPath,
		"ResolveHarpCanonicalTranscriptPath": ResolveHarpCanonicalTranscriptPath,
	}

	check := func(t *testing.T, name string, got string, err error) {
		t.Helper()
		require.Error(t, err, "%s must fail when the home directory cannot be resolved", name)
		assert.Empty(t, got, "%s must not return a path alongside its error", name)
		assert.Contains(t, err.Error(), bare,
			"%s dropped the underlying cause; the reason home resolution failed must survive wrapping", name)
		assert.NotEqual(t, bare, err.Error(),
			"%s returns os.UserHomeDir's error verbatim, so its failure is indistinguishable "+
				"from every other home-rooted resolver's", name)
	}

	for name, fn := range noArg {
		t.Run(name, func(t *testing.T) {
			got, err := fn()
			check(t, name, got, err)
		})
	}
	for name, fn := range harpArg {
		t.Run(name, func(t *testing.T) {
			got, err := fn("swift-amber-falcon")
			check(t, name, got, err)
		})
	}
}

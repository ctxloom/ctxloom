package paths

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every Home*/cache accessor reports a resolution failure through one format,
// and until this test nothing read that text — the strings survived a refactor
// that collapsed ten copies of the body purely because the refactor was careful.
// That is the unchecked-binding shape: prose the code emits and no test names.
//
// The expectation is BUILT FROM the same constants the production code formats
// with, never retyped. A copied literal here would keep passing after someone
// reworded the real message, which is the failure this asserts against.
func TestHomeAccessorErrorsNameTheStoreAndTheExactPath(t *testing.T) {
	// os.UserHomeDir fails when HOME is empty, which is the only branch that
	// produces these errors.
	t.Setenv("HOME", "")

	for _, tc := range []struct {
		name     string
		call     func() (string, error)
		what     string
		segments []string
	}{
		{"sessions", HomeSessionsDir, whatHomeSessions, []string{SessionsDir}},
		{"logs", HomeLogsDir, whatHomeLogs, []string{LogsDir}},
		{"trigger cache", TriggerCacheDir, whatTriggerCache, []string{CacheDir, TriggersDir}},
		{"coord", HomeCoordDir, whatHomeCoord, []string{CoordDirName}},
		{"locks", HomeLocksDir, whatHomeLocks, []string{HomeLocksDirName}},
		{"approvals", HomeApprovalsPath, whatHomeApprovals, []string{ApprovalsDirName}},
		{"companion consent", HomeCompanionConsentPath, whatCompanionConsent, []string{CompanionConsentFileName + ".yaml"}},
		{"allowed signers", HomeAllowedSignersPath, whatAllowedSigners, []string{AllowedSignersFileName}},
		{"distrusted signers", HomeDistrustedSignersPath, whatDistrustedSigners, []string{DistrustedSignersFileName}},
		{"engine token", func() (string, error) { return HomeEngineTokenPath("claude-code") }, whatEngineToken, []string{HomeAuthDirName, "claude-code" + EngineTokenExt}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.call()
			require.Error(t, err, "an unresolvable home must not yield a usable path")
			require.Empty(t, got, "a failed resolution must return no path at all")

			// Rebuild the message from the SAME constants, wrapping whatever
			// os.UserHomeDir actually reported, so this pins the format, the
			// store's description AND its exact path segments together.
			want := fmt.Errorf(homeUnderErrFormat, tc.what, AppDirName, filepath.Join(tc.segments...), errors.Unwrap(err))
			require.EqualError(t, err, want.Error())

			// The cause must stay reachable: callers distinguish "no home" from
			// a ctxloom-level refusal by unwrapping, never by reading the text.
			require.NotNil(t, errors.Unwrap(err), "the underlying cause must remain unwrappable")
		})
	}
}

// The description constants' VALUES, pinned as literals exactly once.
//
// This is deliberately the one place a literal appears, and it is not a
// magic-string lapse — it is the only assertion that can catch a REWORDING.
// The table above builds its expectation from the same constant the production
// code formats with, which pins the FORMAT but is tautological about the text:
// measured by mutation, changing whatHomeSessions to "the sessions root" left
// that table green. Pinning the value here makes a reword fail loudly, so it
// becomes a deliberate edit rather than a silent drift in what users read.
func TestHomeStoreDescriptionsAreTheWordsWeThinkTheyAre(t *testing.T) {
	for name, got := range map[string]string{
		"the home sessions root":          whatHomeSessions,
		"the home logs root":              whatHomeLogs,
		"the trigger verdict cache":       whatTriggerCache,
		"the coordinator state root":      whatHomeCoord,
		"the home lock directory":         whatHomeLocks,
		"the user countersignature store": whatHomeApprovals,
		"the companion consent record":    whatCompanionConsent,
		"the user trust root":             whatAllowedSigners,
		"the user distrust record":        whatDistrustedSigners,
		"the home records directory":      whatHomeRecords,
		"the stored engine token":         whatEngineToken,
	} {
		require.Equal(t, name, got, "a store's description changed; update the message deliberately, not by accident")
	}

	// Distinctness matters as much as the wording: two accessors sharing a
	// description would make their errors indistinguishable to a reader, and
	// would hide an accessor wired to the wrong constant.
	seen := map[string]bool{}
	for _, d := range []string{
		whatHomeSessions, whatHomeLogs, whatTriggerCache, whatHomeCoord, whatHomeLocks,
		whatHomeApprovals, whatCompanionConsent, whatAllowedSigners, whatDistrustedSigners, whatHomeRecords, whatEngineToken,
	} {
		require.False(t, seen[d], "two stores share the description %q", d)
		seen[d] = true
	}
}

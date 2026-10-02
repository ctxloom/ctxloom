package coord

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// Shared spool test helpers, and the projection's exhaustiveness pin.
//
// Every spool test redirects HOME first. The spool resolves through
// paths.HarpPersistDir, which resolves against $HOME, so a test that forgot
// would write its fixtures into the developer's real session store and pass —
// the residue only surfacing later as a spool full of "child-harp-1".

// teeHome is spooltest.TeeHome, named the way this suite has always called it.
func teeHome(t *testing.T) string { return spooltest.TeeHome(t) }

// spoolEntries is spooltest.Entries.
func spoolEntries(t *testing.T, harp string, dir spool.Dir) []spool.Entry {
	return spooltest.Entries(t, harp, dir)
}

// spoolDelivered is spooltest.Delivered.
func spoolDelivered(t *testing.T, harp string) map[string]time.Time {
	return spooltest.Delivered(t, harp)
}

// awaitDelivered is spooltest.AwaitDelivered at this suite's wait.
func awaitDelivered(t *testing.T, harp, identity, why string) {
	t.Helper()
	spooltest.AwaitDelivered(t, harp, identity, conformanceWait, why)
}

// awaitDeliveredCount waits for harp's delivered record to hold exactly n
// identities.
func awaitDeliveredCount(t *testing.T, harp string, n int, why string) map[string]time.Time {
	t.Helper()
	var got map[string]time.Time
	require.Eventually(t, func() bool {
		got = spoolDelivered(t, harp)
		return len(got) == n
	}, conformanceWait, 10*time.Millisecond, "%s: %s's delivered record should hold %d identit(ies), holds %d", why, harp, n, len(got))
	return got
}

// awaitRunnerHome waits for the migrated path's runner half to exist. AgentRun
// returns once the run is ENQUEUED; the runner is spawned and dials home
// afterwards, so reaching for it immediately finds nothing.
func awaitRunnerHome(t *testing.T, c *Coordinator, sp *fakeSpawner, harp string) TestHome {
	t.Helper()
	require.Eventually(t, func() bool {
		return sp.engineHome(0) != nil && rosterState(c, harp) == StateIdle
	}, conformanceWait, 10*time.Millisecond, "the migrated child's runner never came up")
	home := sp.engineHome(0)
	require.NotNil(t, home)
	return home
}

// TestSpoolKindMapping_IsExhaustive is the completeness gate: every kind
// that can ride the mailbox has a frontmatter spelling, the mapping round
// trips, and a kind nobody mapped is an ERROR rather than a silent
// pass-through.
func TestSpoolKindMapping_IsExhaustive(t *testing.T) {
	kinds := MailKinds()
	require.NotEmpty(t, kinds, "the exhaustiveness authority itself must not be empty")
	require.Contains(t, kinds, KindUnset, "KindUnset stays a member of the closed vocabulary (a Message whose Kind was never set) even though no sender or coordinator-internal path can produce one anymore")

	seen := make(map[string]string, len(kinds))
	for _, k := range kinds {
		spoolKind, err := SpoolKindForMail(k)
		require.NoError(t, err, "mail kind %q has no frontmatter spelling", k)
		require.NotEmpty(t, spoolKind, "a frontmatter kind may never be empty: spool.Writer refuses one")

		back, err := MailKindForSpool(spoolKind)
		require.NoError(t, err, "frontmatter kind %q does not map back", spoolKind)
		assert.Equal(t, k, back, "the mapping must round trip for %q", k)

		if prev, dup := seen[spoolKind]; dup {
			t.Fatalf("mail kinds %q and %q share the frontmatter spelling %q", prev, k, spoolKind)
		}
		seen[spoolKind] = k
	}

	assert.NotEqual(t, KindMessage, seen[SpoolKindUnkinded],
		"the unkinded message must keep a spelling of its own: it renders no provenance header, a %q does", KindMessage)

	// A synthetic new kind must FAIL, not travel unmapped.
	_, err := SpoolKindForMail("a_kind_nobody_mapped")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no frontmatter representation")
	_, err = MailKindForSpool("a_frontmatter_kind_nobody_mapped")
	require.Error(t, err)
}

// spoolDirsUnder is spooltest.DirsUnder.
func spoolDirsUnder(t *testing.T, root string) []string { return spooltest.DirsUnder(t, root) }

// writeSpoolMail puts one message file straight into harp's in/ spool the way
// the coordinator's courier would, spelling the mailbox kind for the file.
func writeSpoolMail(t *testing.T, harp, from, kind, body string) {
	t.Helper()
	spoolKind, err := SpoolKindForMail(kind)
	require.NoError(t, err)
	spooltest.WriteMail(t, harp, from, spoolKind, body, spoolWriterIDCoordinator)
}

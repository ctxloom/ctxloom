package coord

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
)

// Shared spool test helpers, and the projection's exhaustiveness pin.
//
// Every spool test redirects HOME first. The spool resolves through
// paths.HarpPersistDir, which resolves against $HOME, so a test that forgot
// would write its fixtures into the developer's real session store and pass —
// the residue only surfacing later as a spool full of "child-harp-1".

// teeHome gives THIS test a private HOME and returns it. Every spool path
// hangs off HOME, and the owner's harp is a constant across tests, so two
// tests sharing a HOME would read each other's owner spool — the second one
// receiving results the first one's children wrote. It is idempotent per
// test (the constructors call it, and a test that wants the path calls it
// too): a second call returns the HOME the first minted rather than
// switching the test to a directory it has already been told about.
//
// The marker is a key t.Setenv restores at the test's end, so it can never
// name another test; it is kept outside the CTXLOOM_* namespace because
// testsupport.Isolate clears that whole namespace.
func teeHome(t *testing.T) string {
	t.Helper()
	const marker = "COORD_TEST_HOME_OWNER"
	if os.Getenv(marker) == t.Name() {
		return os.Getenv("HOME")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(marker, t.Name())
	return home
}

// spoolEntries sweeps a spool directory, treating "the directory does not
// exist" as "no messages" rather than as an error.
//
// The distinction matters for the failure tests: a tee that could not build
// its writer never created the directory, and the assertion those tests make
// is about the ABSENCE of files, which a sweep error would mask as a different
// failure entirely.
func spoolEntries(t *testing.T, harp string, dir spool.Dir) []spool.Entry {
	t.Helper()
	path, err := spool.DirPath(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
		return nil
	}
	res, err := spool.Sweep(spool.NewHomeMapper(), harp, dir)
	require.NoError(t, err)
	require.NoError(t, res.ProblemErr())
	return res.Entries
}

// awaitRunnerHome waits for the migrated path's runner half to exist. AgentRun
// returns once the run is ENQUEUED; the runner is spawned and dials home
// afterwards, so reaching for it immediately finds nothing.
func awaitRunnerHome(t *testing.T, c *Coordinator, sp *fakeSpawner, harp string) *Home {
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

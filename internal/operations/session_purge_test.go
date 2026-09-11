package operations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedPurgeableSession mints a session, ends it, and gives it a transcript
// and an essence — the shape every destroyer is allowed to act on. The lock
// file it leaves behind is FREE: exactly what the kernel leaves once the
// owning process has ended, however it ended.
func seedPurgeableSession(t *testing.T) (harp, transcript string) {
	t.Helper()
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	t.Cleanup(func() { sessionlock.Release(entry.HarpName) })
	require.NoError(t, EndSession(entry.HarpName, time.Now()))
	return entry.HarpName, seedPurgeBulk(t, entry.HarpName)
}

// seedCrashedSession mints a session whose process DIED before EndSession:
// the index entry never received an ended_at, and the lock file is present
// and FREE — the kernel drops an flock with its holder, however it went. That
// is the provably-dead session the index alone would call live forever.
func seedCrashedSession(t *testing.T) (harp, transcript string) {
	t.Helper()
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	require.Nil(t, entry.EndedAt, "the fixture is a session that never ended")
	sessionlock.Release(entry.HarpName)
	return entry.HarpName, seedPurgeBulk(t, entry.HarpName)
}

// seedRunningSession mints a session that is RUNNING right now: no ended_at,
// and the lock held by this very process for the test's lifetime.
func seedRunningSession(t *testing.T) (harp, transcript string) {
	t.Helper()
	testsupport.Isolate(t)
	entry, err := AssignSessionHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	require.Nil(t, entry.EndedAt, "the fixture is a session that never ended")
	t.Cleanup(func() { sessionlock.Release(entry.HarpName) })
	return entry.HarpName, seedPurgeBulk(t, entry.HarpName)
}

// seedPurgeBulk writes harp a transcript and an essence and returns the
// transcript path — the machine bulk and the derived artifact a destroyer
// may act on.
func seedPurgeBulk(t *testing.T, harp string) (transcript string) {
	t.Helper()
	transcript, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(transcript), 0o755))
	require.NoError(t, os.WriteFile(transcript, []byte(`{"type":"user","content":"hello"}`+"\n"), 0o644))
	essence, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essence, []byte("# essence\n"), 0o644))
	return transcript
}

func purgeTranscript(harp string, evenIfLive bool) (*PurgeSessionResult, error) {
	return PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{PurgePopulationTranscript},
		EvenIfLive:  evenIfLive,
		Apply:       true,
	})
}

// TestPurgeSession_DeadOwnerIsDestroyed is the one permitted case: the lock
// exists and nothing holds it, so the owner has provably ended.
func TestPurgeSession_DeadOwnerIsDestroyed(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)

	res, err := purgeTranscript(harp, false)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.NoFileExists(t, transcript)
}

// TestPurgeSession_HeldLockRefuses: the session was ended in the index and
// then RESUMED, so its lock is held again while ended_at still reads as set.
// The index cannot see this; the lock can. Nothing is destroyed.
//
// MUTATION: invert the lock sense (treat Alive as permission) → red.
func TestPurgeSession_HeldLockRefuses(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	require.NoError(t, sessionlock.Hold(harp))

	_, err := purgeTranscript(harp, false)
	require.ErrorIs(t, err, ErrPurgeOwnerNotProvenDead)
	assert.FileExists(t, transcript, "a refusal destroys nothing")
}

// TestPurgeSession_NoLockRefuses: a session from before the lock existed has
// no lock file at all. "Cannot determine" is never permission.
//
// MUTATION: treat Indeterminate as permission → red.
func TestPurgeSession_NoLockRefuses(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(lock))

	_, err = purgeTranscript(harp, false)
	require.ErrorIs(t, err, ErrPurgeOwnerNotProvenDead)
	assert.FileExists(t, transcript, "a refusal destroys nothing")
}

// TestPurgeSession_CrashedOwnerIsDestroyed: ended_at is a timestamp, not a
// liveness oracle. A session that died before EndSession has none, and the
// index alone would call it live forever; its FREE lock proves the owner
// gone, and that proof is sufficient — no escape flag required.
//
// MUTATION: let a nil ended_at refuse ahead of the lock → red.
func TestPurgeSession_CrashedOwnerIsDestroyed(t *testing.T) {
	harp, transcript := seedCrashedSession(t)

	res, err := purgeTranscript(harp, false)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.NoFileExists(t, transcript)
}

// TestPurgeSession_RunningOwnerRefuses is the case the crashed-session rule
// must never widen into: no ended_at AND a held lock is a session writing
// its transcript right now. The lock refuses; the missing ended_at plays no
// part in either direction.
//
// MUTATION: skip the lock probe when ended_at is nil → red.
func TestPurgeSession_RunningOwnerRefuses(t *testing.T) {
	harp, transcript := seedRunningSession(t)

	_, err := purgeTranscript(harp, false)
	require.ErrorIs(t, err, ErrPurgeOwnerNotProvenDead)
	assert.FileExists(t, transcript, "a refusal destroys nothing")
}

// TestPurgeSession_NeverEndedNoLockRefuses: a session from before the lock
// existed that also never reached EndSession has NO liveness signal at all.
// With ended_at out of the decision, the lock's Indeterminate is what refuses
// it — "cannot determine" is never permission.
//
// MUTATION: treat Indeterminate as permission → red.
func TestPurgeSession_NeverEndedNoLockRefuses(t *testing.T) {
	harp, transcript := seedCrashedSession(t)
	lock, err := paths.HarpLockPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.Remove(lock))

	_, err = purgeTranscript(harp, false)
	require.ErrorIs(t, err, ErrPurgeOwnerNotProvenDead)
	assert.FileExists(t, transcript, "a refusal destroys nothing")
}

// TestPurgeSession_EvenIfLiveOverridesBothRefusals: the escape is the
// caller's explicit, deliberate assertion, and it covers every verdict the
// lock cannot turn into permission — a held lock AND no lock at all.
//
// MUTATION: make EvenIfLive the default → the two refusal tests above go red;
// this one keeps passing, which is why it never stands alone.
func TestPurgeSession_EvenIfLiveOverridesBothRefusals(t *testing.T) {
	t.Run("held lock", func(t *testing.T) {
		harp, transcript := seedPurgeableSession(t)
		require.NoError(t, sessionlock.Hold(harp))

		res, err := purgeTranscript(harp, true)
		require.NoError(t, err)
		assert.True(t, res.Applied)
		assert.NoFileExists(t, transcript)
	})
	t.Run("no lock", func(t *testing.T) {
		harp, transcript := seedPurgeableSession(t)
		lock, err := paths.HarpLockPath(harp)
		require.NoError(t, err)
		require.NoError(t, os.Remove(lock))

		res, err := purgeTranscript(harp, true)
		require.NoError(t, err)
		assert.True(t, res.Applied)
		assert.NoFileExists(t, transcript)
	})
	// The escape must REACH a session that never ended: nothing ahead of the
	// lock may refuse on the index's say-so, or --even-if-live is a flag
	// that cannot do what its name promises.
	t.Run("running, never ended", func(t *testing.T) {
		harp, transcript := seedRunningSession(t)

		res, err := purgeTranscript(harp, true)
		require.NoError(t, err)
		assert.True(t, res.Applied)
		assert.NoFileExists(t, transcript)
	})
}

// TestPurgeSession_PlanAlsoRefuses: the report-only run tells the caller
// BEFORE --yes that the session would be refused, so the refusal is never a
// surprise that arrives only once the human has typed the destructive flag.
func TestPurgeSession_PlanAlsoRefuses(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	require.NoError(t, sessionlock.Hold(harp))

	_, err := PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{PurgePopulationTranscript},
	})
	require.ErrorIs(t, err, ErrPurgeOwnerNotProvenDead)
	assert.FileExists(t, transcript)
}

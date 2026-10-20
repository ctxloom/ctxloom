package operations

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// A transcript is the session's record: PurgeSession refuses to destroy one
// that Retention keeps — a session that ended within the retention period,
// or one marked keep — in the report-only run and the applied one alike,
// and the refusal is typed (no caller reads its text to tell the two
// apart). Nothing is destroyed.
func TestPurgeSession_RetentionRefusesARetainedTranscript(t *testing.T) {
	for name, tc := range map[string]struct {
		keep      bool
		retention sessions.Retention
		want      error
	}{
		"ended within the retention period": {retention: sessions.Retention{Cutoff: time.Now().Add(-time.Hour)}, want: sessions.ErrTranscriptRecent},
		"marked keep":                       {keep: true, retention: releasedRetention(), want: sessions.ErrTranscriptKept},
		"no retention stated":               {retention: sessions.Retention{}, want: sessions.ErrNoRetentionBound},
	} {
		t.Run(name, func(t *testing.T) {
			harp, transcript := seedPurgeableSession(t)
			if tc.keep {
				require.NoError(t, SetSessionKeep(harp, true))
			}
			for _, apply := range []bool{false, true} {
				_, err := PurgeSession(harp, PurgeSessionRequest{
					Harp:        harp,
					Populations: []PurgePopulation{PurgePopulationTranscript},
					Retention:   tc.retention,
					Apply:       apply,
				})
				require.ErrorIs(t, err, ErrPurgeRetained)
				require.ErrorIs(t, err, tc.want)
				assert.FileExists(t, transcript)
			}
		})
	}
}

// Once retention releases it — the session ended at or before the cutoff and
// is not marked keep — the transcript goes; `session keep --undo` is what
// releases a kept one.
func TestPurgeSession_RetentionReleasedTranscriptIsDestroyed(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	require.NoError(t, SetSessionKeep(harp, true))
	require.NoError(t, SetSessionKeep(harp, false))

	res, err := purgeTranscript(harp, false)
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.NoFileExists(t, transcript)
}

// Marking a session keep and releasing it again writes its record, not its
// conversation: the release must leave the transcript as reclaimable as it
// was before the mark, not restart its retention.
func TestPurgeSession_KeepThenUndoDoesNotRestartRetention(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	long := time.Now().Add(-90 * 24 * time.Hour)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	require.NoError(t, mgr.MarkEnded(harp, long))
	require.NoError(t, os.Chtimes(transcript, long, long))
	require.NoError(t, SetSessionKeep(harp, true))
	require.NoError(t, SetSessionKeep(harp, false))

	res, err := PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{PurgePopulationTranscript},
		Retention:   sessions.Retention{Cutoff: time.Now().Add(-30 * 24 * time.Hour)},
		Apply:       true,
	})
	require.NoError(t, err)
	assert.True(t, res.Applied)
	assert.NoFileExists(t, transcript)
}

// A refusal still carries the plan's header: the CLI emits the result beside
// the refusal, so it says which harp and which destroyer refused.
func TestPurgeSession_RetentionRefusalCarriesTheResult(t *testing.T) {
	harp, _ := seedPurgeableSession(t)
	res, err := PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{PurgePopulationTranscript},
		Retention:   sessions.Retention{Cutoff: time.Now().Add(-time.Hour)},
	})
	require.ErrorIs(t, err, ErrPurgeRetained)
	require.NotNil(t, res)
	assert.Equal(t, harp, res.Harp)
	assert.Equal(t, []PurgePopulation{PurgePopulationTranscript}, res.Populations)
}

// The retention guard protects a real file: a retained session whose
// transcript is already gone is not refused forever, it plans nothing.
func TestPurgeSession_RetentionAsksOnlyWhenATranscriptIsThere(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)
	require.NoError(t, os.Remove(transcript))

	res, err := PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{PurgePopulationTranscript},
		Retention:   sessions.Retention{Cutoff: time.Now().Add(-time.Hour)},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Destroy)
}

// Retention guards the transcript population only: a request that does not
// ask for it destroys no transcript, so a retained one does not refuse it.
func TestPurgeSession_RetentionAsksOnlyWhenTheTranscriptIsWanted(t *testing.T) {
	harp, transcript := seedPurgeableSession(t)

	res, err := PurgeSession(harp, PurgeSessionRequest{
		Harp:        harp,
		Populations: []PurgePopulation{"not-the-transcript"},
		Retention:   sessions.Retention{Cutoff: time.Now().Add(-time.Hour)},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Destroy)
	assert.NotEmpty(t, res.Keep, "the transcript is kept, and named")
	assert.FileExists(t, transcript)
}

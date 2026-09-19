package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetCurrentSessionViaGetSession_SortsUnsortedInput pins the collapsed
// GetCurrentSessionViaGetSession (merged from the
// SortSessionsMostRecentFirst/MostRecentSession/GetCurrentSessionViaListSessions
// chain) against a list() that returns sessions NOT already most-recent-first.
// The doc comment on the old MostRecentSession assumed "a ListSessions result
// (already sorted most recent first)" — an invariant enforced nowhere at the
// time SortSessionsMostRecentFirst had zero callers. This proves the merged
// function now enforces the ordering itself rather than trusting the caller.
func TestGetCurrentSessionViaGetSession_SortsUnsortedInput(t *testing.T) {
	older := SessionMeta{ID: "older", StartTime: time.Unix(100, 0)}
	newer := SessionMeta{ID: "newer", StartTime: time.Unix(200, 0)}

	list := func(string) ([]SessionMeta, error) {
		// Deliberately UNSORTED: the older session listed first.
		return []SessionMeta{older, newer}, nil
	}
	var gotID string
	getSession := func(workDir, id string) (*Session, error) {
		gotID = id
		return &Session{ID: id}, nil
	}

	sess, err := GetCurrentSessionViaGetSession("/work", list, getSession)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, "newer", gotID, "the most recent session by StartTime must be loaded regardless of list() order")
}

func TestGetCurrentSessionViaGetSession_PropagatesListError(t *testing.T) {
	wantErr := errors.New("list failed")
	list := func(string) ([]SessionMeta, error) { return nil, wantErr }
	getSession := func(workDir, id string) (*Session, error) { return nil, nil }

	_, err := GetCurrentSessionViaGetSession("/work", list, getSession)
	assert.Equal(t, wantErr, err)
}

func TestGetCurrentSessionViaGetSession_NoSessionsErrors(t *testing.T) {
	list := func(string) ([]SessionMeta, error) { return nil, nil }
	getSession := func(workDir, id string) (*Session, error) { return nil, nil }

	_, err := GetCurrentSessionViaGetSession("/work", list, getSession)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no sessions found")
}

// "This project has no history" and "the history could not be read"
// are different facts, and a bare fmt.Errorf gave a caller no way to tell them
// apart except by matching the message text. The empty-history case must be
// recognisable with errors.Is; a real list failure must NOT be, so a caller
// that treats absence as normal cannot accidentally swallow a fault.
func TestGetCurrentSessionViaGetSession_NoSessionsIsASentinel(t *testing.T) {
	getSession := func(workDir, id string) (*Session, error) { return nil, nil }

	empty := func(string) ([]SessionMeta, error) { return nil, nil }
	_, err := GetCurrentSessionViaGetSession("/work", empty, getSession)
	assert.ErrorIs(t, err, ErrNoSessions, "honest absence must be discriminable without string matching")

	boom := errors.New("history store unreadable")
	failing := func(string) ([]SessionMeta, error) { return nil, boom }
	_, err = GetCurrentSessionViaGetSession("/work", failing, getSession)
	assert.NotErrorIs(t, err, ErrNoSessions, "a read failure must never read as honest absence")
	assert.ErrorIs(t, err, boom)
}

// A transcript whose every line fails parseLine is indistinguishable from an
// empty one, because ParseSessionFile returns an empty Session and a nil
// error for both and hands the caller no counts. That does not hold: parseLine
// is the CALLER's closure, invoked once per non-empty line, so the caller
// already owns the only counts it needs - which is exactly how
// transcript.ParseTranscriptFile discriminates the two states without
// ParseSessionFile changing shape.
//
// This pins both facts at once: the degrade-to-partial contract (nil error,
// no entries - deliberate, so that one corrupt line never destroys a readable
// session) AND the per-line callback count that makes the contract safe.
// Widening the return signature would break the first to supply the second.
func TestParseSessionFile_UnparseableLinesAreCountableByTheCaller(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCalls int
	}{
		{"empty file", "", 0},
		{"blank lines only", "\n  \n\n", 0},
		{"every line unparseable", "garbage\nmore garbage\nstill garbage\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			require.NoError(t, afero.WriteFile(fs, "/t/session.jsonl", []byte(tc.body), 0o600))
			store := SessionStore{FS: fs}

			calls := 0
			sess, err := store.ParseSessionFile("/t/session.jsonl", "sid", func([]byte) []SessionEntry {
				calls++
				return nil
			})

			require.NoError(t, err, "degrade-to-partial: an unreadable line is not a fatal read")
			require.NotNil(t, sess)
			assert.Empty(t, sess.Entries)
			assert.Equal(t, tc.wantCalls, calls,
				"the caller's own closure is the seam that separates 'no content' from 'nothing parsed'")
		})
	}
}

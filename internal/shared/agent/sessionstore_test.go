package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetCurrentSessionViaGetSession_SortsUnsortedInput pins the collapsed
// GetCurrentSessionViaGetSession (U102-F02/F03: merged from the
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

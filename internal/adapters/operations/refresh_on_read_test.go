package operations

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// grownTurn is a turn that exists only past the first captured lines of the
// claude fixture: a read that shows it read the vendor log, not the copy.
const grownTurn = "[Request interrupted by user for tool use]"

// grownTitle is the fixture's first main-thread user prompt.
const grownTitle = "GOAL: Truthfulness audit"

// seedGrownClaudeHarp indexes a claude session bound to a vendor log holding
// the fixture's first captured lines, captures it once, then grows the log to
// the whole fixture: a session whose run did not exit through ctxloom after
// its last capture. captured == 0 seeds a session never captured at all: its
// run was killed before ctxloom saw it exit. It returns the session's entry as
// the index reads it.
func seedGrownClaudeHarp(t *testing.T, captured int) (*sessions.Manager, sessions.Entry) {
	t.Helper()
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	minted, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.RecordEngineVersion(minted.HarpName, stubEngineVersion))
	vendorPath := vendorFileWithLines(t, captured)
	require.NoError(t, mgr.BindSession(minted.HarpName, "sess-1", vendorPath))
	entry, err := mgr.Find(minted.HarpName)
	require.NoError(t, err)
	require.NotNil(t, entry)

	if captured > 0 {
		_, err = RefreshVendorTranscript(context.Background(), safefs.New(), engines.Registry(), *entry)
		require.NoError(t, err)
	}

	whole, err := os.ReadFile(claudeFixturePath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(vendorPath, whole, 0o644))

	entry, err = mgr.Find(minted.HarpName)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return mgr, *entry
}

func entriesContain(entries []agent.SessionEntry, s string) bool {
	for _, e := range entries {
		if strings.Contains(e.Content, s) {
			return true
		}
	}
	return false
}

// A resume reads the session as its engine left it: the turns the vendor log
// gained after its last capture are in what primes the resumed run.
func TestRecordedSessionEntries_ReadsTheTurnsTheVendorLogGainedSinceItsLastCapture(t *testing.T) {
	testsupport.Isolate(t)
	_, entry := seedGrownClaudeHarp(t, 6)
	require.NotContains(t, strings.Join(canonicalLines(t, entry.HarpName), "\n"), grownTurn,
		"the fixture's last capture must predate the grown turn")

	entries, err := RecordedSessionEntries(context.Background(), safefs.New(), engines.Registry(), entry.HarpName)
	require.NoError(t, err)
	assert.True(t, entriesContain(entries, grownTurn),
		"a resume must refresh the canonical transcript from the vendor log before reading it")
}

// A refresh that fails on a read path warns and the read goes on with the
// copy the session already has: the resume is not blocked, and the copy is
// what it was.
func TestRecordedSessionEntries_FailedRefreshWarnsAndResumesFromTheCopyItHad(t *testing.T) {
	testsupport.Isolate(t)
	mgr, entry := seedGrownClaudeHarp(t, 6)
	before := canonicalLines(t, entry.HarpName)
	require.NotEmpty(t, before)
	// A version no claude adapter carries: the refresh refuses.
	require.NoError(t, mgr.RecordEngineVersion(entry.HarpName, "0.0.1"))

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	entries, err := RecordedSessionEntries(context.Background(), safefs.New(), engines.Registry(), entry.HarpName)
	require.NoError(t, err, "a failed refresh must not block the read")
	assert.NotEmpty(t, entries, "the read goes on with the copy the session has")
	assert.False(t, entriesContain(entries, grownTurn), "nothing was refreshed, so the copy is the last capture")
	assert.Contains(t, warnings.String(), ReadRefreshFailed)
	assert.Contains(t, warnings.String(), entry.HarpName)
	assert.Equal(t, before, canonicalLines(t, entry.HarpName), "a failed refresh leaves the copy it had")
}

// A purged session stays purged when it is read: its vendor log may outlive
// the purge, and a read must not rebuild the transcript the purge destroyed.
func TestRefreshForRead_LeavesAPurgedSessionPurged(t *testing.T) {
	testsupport.Isolate(t)
	mgr, entry := seedGrownClaudeHarp(t, 6)
	require.NoError(t, mgr.MarkPurged(entry.HarpName, time.Now().UTC()))
	canon, err := paths.HarpCanonicalTranscriptPath(entry.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.Remove(canon))

	got, err := RefreshForRead(context.Background(), safefs.New(), engines.Registry(), entry.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
	_, statErr := os.Stat(canon)
	assert.True(t, os.IsNotExist(statErr), "a read must not rebuild a purged session's transcript")
}

// A listing titles a session whose last capture held no user prompt yet from
// its vendor log as it is now.
func TestViewSessions_RefreshesAnUntitledRowBeforeTitlingIt(t *testing.T) {
	testsupport.Isolate(t)
	_, entry := seedGrownClaudeHarp(t, 3)
	require.Empty(t, SessionTitle(entry), "the fixture's last capture must hold no user prompt")

	views := ViewSessions([]sessions.Entry{entry}, nil, ReadRefresh(context.Background(), safefs.New(), engines.Registry()))
	require.Len(t, views, 1)
	assert.True(t, strings.HasPrefix(views[0].Title, grownTitle), "got title %q", views[0].Title)
}

// A listing titles a session never captured at all from its vendor log: the
// row reads the transcript the refresh wrote, not the entry listed before it.
func TestViewSessions_TitlesASessionNeverCaptured(t *testing.T) {
	testsupport.Isolate(t)
	_, entry := seedGrownClaudeHarp(t, 0)
	require.Empty(t, entry.CanonicalTranscriptPath, "the fixture must never have been captured")

	views := ViewSessions([]sessions.Entry{entry}, nil, ReadRefresh(context.Background(), safefs.New(), engines.Registry()))
	require.Len(t, views, 1)
	assert.True(t, strings.HasPrefix(views[0].Title, grownTitle), "got title %q", views[0].Title)
}

// A titled row is not refreshed by a listing: a title is the first prompt,
// which the copy already has, so a listing pays no refresh for it.
func TestViewSessions_LeavesATitledRowsCopyAlone(t *testing.T) {
	testsupport.Isolate(t)
	_, entry := seedGrownClaudeHarp(t, 6)
	require.NotEmpty(t, SessionTitle(entry))
	before := canonicalLines(t, entry.HarpName)

	views := ViewSessions([]sessions.Entry{entry}, nil, ReadRefresh(context.Background(), safefs.New(), engines.Registry()))
	require.Len(t, views, 1)
	assert.True(t, strings.HasPrefix(views[0].Title, grownTitle), "got title %q", views[0].Title)
	assert.Equal(t, before, canonicalLines(t, entry.HarpName), "a titled row's copy is not rewritten by a listing")
}

// A row whose session left the index after it was listed keeps the entry it
// was listed with.
func TestReadRefresh_KeepsARowWhoseSessionLeftTheIndex(t *testing.T) {
	testsupport.Isolate(t)
	listed := sessions.Entry{HarpName: "listed-then-removed", Backend: "claude-code"}

	got := ReadRefresh(context.Background(), safefs.New(), engines.Registry())(listed)
	assert.Equal(t, listed, got)
}

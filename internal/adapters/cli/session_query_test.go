package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestAllWordsMatch pins the AND-across-words semantics `session query`
// promises in its help text: every word must be present, not just one.
func TestAllWordsMatch(t *testing.T) {
	haystack := "swift-amber-falcon fixed the harp bind race"

	assert.True(t, allWordsMatch(haystack, []string{"harp"}))
	assert.True(t, allWordsMatch(haystack, []string{"harp", "bind"}), "all given words present -> match")
	assert.True(t, allWordsMatch(haystack, []string{"HARP"}), "case-insensitive")
	assert.False(t, allWordsMatch(haystack, []string{"harp", "nonexistent"}), "one missing word -> no match")
	assert.True(t, allWordsMatch(haystack, nil), "no words required -> vacuously matches")
}

// TestSessionMetadataHaystack pins what a metadata-only query can match
// against: harp name, summary, and the same start/end formatting the row
// renders — so a query for a visible date fragment behaves like the user
// expects.
func TestSessionMetadataHaystack(t *testing.T) {
	started := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	ended := started.Add(time.Hour)
	e := viewTitled(sessions.Entry{HarpName: "swift-amber-falcon", StartedAt: started, EndedAt: &ended}, "Fixed the harp bind race")

	got := sessionMetadataHaystack(e)

	assert.Contains(t, got, "swift-amber-falcon")
	assert.Contains(t, got, "fixed the harp bind race")
	// Compare against sessionTime's own String() (local-zone formatted) rather
	// than a hard-coded UTC clock string, since the haystack must match what a
	// user actually sees in the row, wherever the test happens to run.
	assert.Contains(t, got, strings.ToLower(sessionTime(started).String()), "start renders using the same formatting SessionRow shows")
	assert.Contains(t, got, strings.ToLower(sessionTime(ended).String()), "end renders too, once the session has one")
}

// TestSessionMatchesQuery_MetadataHit: a match found in harp/title.
func TestSessionMatchesQuery_MetadataHit(t *testing.T) {
	e := viewTitled(sessions.Entry{HarpName: "swift-amber-falcon"}, "Fixed the harp bind race")
	assert.True(t, sessionMatchesQuery(e, []string{"bind"}))
	assert.False(t, sessionMatchesQuery(e, []string{"nonexistent-word"}))
}

// TestSessionMatchesQuery_MetadataOnly: a search reads the row's metadata —
// harp, title, times — and nothing else. A summary file an older ctxloom left
// in the output dir is never read, so a word only there does not match.
func TestSessionMatchesQuery_MetadataOnly(t *testing.T) {
	testsupport.Isolate(t)
	harp := "plump-loose-sash"
	seedLeftoverSummaryWith(t, harp, "## Open Items\n\nInvestigate the retry backoff overflow.\n")

	e := viewTitled(sessions.Entry{HarpName: harp}, "Session wrap-up")

	assert.True(t, sessionMatchesQuery(e, []string{"wrap-up"}), "a word in the title matches")
	assert.False(t, sessionMatchesQuery(e, []string{"backoff"}), "a word only in a leftover summary file does not")
}

// TestRunSessionQuery_Integration drives the real cobra command tree end to
// end: two sessions in the project, one whose title (its first user prompt)
// matches and one that does not. The JSON rows carry the lean listing shape.
func TestRunSessionQuery_Integration(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	hit := recordPrompts(t, dir, "Root-cause the flaky retry-backoff-overflow test")
	recordPrompts(t, dir, "Unrelated housekeeping")

	out, err := runSessionCmd(t, "session", "search", "retry-backoff-overflow", "--format", "json")
	require.NoError(t, err)

	rows := jsonRowsByHarp(t, out)
	require.Len(t, rows, 1, "only the title-matching session is returned")
	require.Contains(t, rows, hit)
	assert.Contains(t, rows[hit], "summary")
	assert.Contains(t, rows[hit], "start")
	assert.NotContains(t, rows[hit], "essence_path")
}

// TestRunSessionQuery_NoMatches pins the empty-result path in both formats:
// json yields an empty array (not null, not an error), text yields the same
// "(no sessions)" placeholder `session list` uses.
func TestRunSessionQuery_NoMatches(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open(nil)
	require.NoError(t, err)
	_, err = mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)

	t.Run("json", func(t *testing.T) {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.SetArgs([]string{"session", "search", "no-such-word-anywhere", "--format", "json"})
		t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
		require.NoError(t, rootCmd.Execute())
		assert.Equal(t, "[]", strings.TrimSpace(out.String()))
	})

	t.Run("text", func(t *testing.T) {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&bytes.Buffer{})
		// Explicit --format text: the persistent --format flag is a single
		// shared pflag.Value on rootCmd, so it carries whatever the previous
		// subtest last set unless this invocation overrides it too.
		rootCmd.SetArgs([]string{"session", "search", "no-such-word-anywhere", "--format", "text"})
		t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
		require.NoError(t, rootCmd.Execute())
		assert.Contains(t, out.String(), "(no sessions)")
	})
}

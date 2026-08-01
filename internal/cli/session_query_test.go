package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
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
	e := &sessions.Entry{HarpName: "swift-amber-falcon", Summary: "Fixed the harp bind race", StartedAt: started, EndedAt: &ended}

	got := sessionMetadataHaystack(e)

	assert.Contains(t, got, "swift-amber-falcon")
	assert.Contains(t, got, "fixed the harp bind race")
	// Compare against sessionTime's own String() (local-zone formatted) rather
	// than a hard-coded UTC clock string, since the haystack must match what a
	// user actually sees in the row, wherever the test happens to run.
	assert.Contains(t, got, strings.ToLower(sessionTime(started).String()), "start renders using the same formatting SessionRow shows")
	assert.Contains(t, got, strings.ToLower(sessionTime(ended).String()), "end renders too, once the session has one")
}

// TestSessionMatchesQuery_MetadataHit covers the fast path: a match found in
// harp/summary never needs to touch the essence file at all (no EssencePath
// resolution is even attempted for an entry with no HarpName-backed dir).
func TestSessionMatchesQuery_MetadataHit(t *testing.T) {
	e := &sessions.Entry{HarpName: "swift-amber-falcon", Summary: "Fixed the harp bind race"}
	assert.True(t, sessionMatchesQuery(e, []string{"bind"}, ""))
	assert.False(t, sessionMatchesQuery(e, []string{"nonexistent-word"}, ""))
}

// TestSessionMatchesQuery_EssenceFallback covers the content-search leg: a
// word absent from harp/summary but present in the distilled essence body
// still matches, and a genuinely absent word (checked against both metadata
// and essence) does not.
func TestSessionMatchesQuery_EssenceFallback(t *testing.T) {
	testsupport.Isolate(t)
	harp := "plump-loose-sash"
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	essencePath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essencePath, []byte("## Open Items\n\nInvestigate the retry backoff overflow.\n"), 0o644))

	e := &sessions.Entry{HarpName: harp, Summary: "Session wrap-up"} // summary carries no hint of "backoff"

	assert.True(t, sessionMatchesQuery(e, []string{"backoff"}, ""), "a word only in the essence body still matches")
	assert.False(t, sessionMatchesQuery(e, []string{"nonexistent-word"}, ""), "a word in neither metadata nor essence doesn't match")
}

// TestSessionMatchesQuery_NotDistilled_NoFallback covers a pending session
// (no essence written yet): the content-search leg must degrade to "no
// match" rather than erroring on a missing file.
func TestSessionMatchesQuery_NotDistilled_NoFallback(t *testing.T) {
	testsupport.Isolate(t)
	e := &sessions.Entry{HarpName: "never-distilled-harp", Summary: "still running"}
	assert.False(t, sessionMatchesQuery(e, []string{"backoff"}, ""))
}

// TestRunSessionQuery_Integration drives the real cobra command tree
// (rootCmd, exactly like a shell invocation) end to end: two sessions in the
// project, one matched by essence content and one that matches neither
// metadata nor essence. Pins that (a) the query filters correctly through
// the full list->match->emit path, (b) the JSON rows carry harp/summary/
// start and NOT the essence body, and (c) `session show` is where the body
// actually lives.
func TestRunSessionQuery_Integration(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)

	hit, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	miss, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)

	hitEssenceDir, err := paths.HarpDir(hit.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(hitEssenceDir, 0o755))
	hitEssencePath, err := paths.HarpEssencePath(hit.HarpName)
	require.NoError(t, err)
	essenceBody := "## Summary\n\nRoot-caused the flaky retry-backoff-overflow test.\n"
	require.NoError(t, os.WriteFile(hitEssencePath, []byte(essenceBody), 0o644))

	missEssenceDir, err := paths.HarpDir(miss.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(missEssenceDir, 0o755))
	missEssencePath, err := paths.HarpEssencePath(miss.HarpName)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(missEssencePath, []byte("## Summary\n\nUnrelated housekeeping.\n"), 0o644))

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"session", "search", "retry-backoff-overflow", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	require.NoError(t, rootCmd.Execute(), "stderr: %s", errOut.String())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output: %s", out.String())
	require.Len(t, rows, 1, "only the essence-matching session should be returned")
	assert.Equal(t, hit.HarpName, rows[0]["harp"])
	assert.Contains(t, rows[0], "summary")
	assert.Contains(t, rows[0], "start")
	assert.Equal(t, hitEssencePath, rows[0]["essence_path"], "the row DOES carry the essence file's path (backend-contract V4)")

	assert.NotContains(t, out.String(), "Root-caused", "the essence body itself must never ride in a lightweight (non-full) query row")
}

// TestRunSessionQuery_NoMatches pins the empty-result path in both formats:
// json yields an empty array (not null, not an error), text yields the same
// "(no sessions)" placeholder `session list` uses.
func TestRunSessionQuery_NoMatches(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
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

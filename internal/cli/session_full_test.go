package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedDistilledEssence writes an essence.md for harp under the isolated
// HOME and returns its path, mirroring how session_query_test.go's
// TestSessionMatchesQuery_EssenceFallback seeds a distilled session.
func seedDistilledEssence(t *testing.T, harp, body string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	essencePath, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(essencePath, []byte(body), 0o644))
	return essencePath
}

// TestNewSessionFullRow_CarriesEssenceBody pins the one thing SessionFullRow
// adds over SessionRow: the complete distilled essence body, read straight
// off disk. A never-distilled session gets an empty Essence and (via the
// embedded SessionRow) an empty EssencePath too.
func TestNewSessionFullRow_CarriesEssenceBody(t *testing.T) {
	testsupport.Isolate(t)

	t.Run("distilled session carries its real essence body", func(t *testing.T) {
		body := "## Summary\n\nRoot-caused the flaky retry-backoff-overflow test.\n"
		essencePath := seedDistilledEssence(t, "plump-loose-sash", body)

		row := newSessionFullRow(sessions.Entry{HarpName: "plump-loose-sash", Summary: "wrap-up"}, "")

		assert.Equal(t, body, row.Essence, "the FULL row must carry the essence file's actual content, not a summary or excerpt")
		assert.Equal(t, essencePath, row.EssencePath)
	})

	t.Run("undistilled session has empty essence and path", func(t *testing.T) {
		row := newSessionFullRow(sessions.Entry{HarpName: "never-distilled-harp"}, "")
		assert.Empty(t, row.Essence)
		assert.Empty(t, row.EssencePath)
	})
}

// TestRenderSessionFullText_IncludesRealBody pins the human-readable --full
// renderer: the essence body text must appear verbatim in the output,
// alongside the harp and essence path — not just a summary line, which is
// all the lean SessionRow table shows.
func TestRenderSessionFullText_IncludesRealBody(t *testing.T) {
	body := "## Open Items\n\nInvestigate the retry backoff overflow.\n"
	rows := []SessionFullRow{
		newSessionFullRowForTest("swift-amber-falcon", "Fixed the bug", body, "/fake/essence.md"),
	}
	var buf bytes.Buffer
	require.NoError(t, renderSessionFullText(&buf, rows))
	out := buf.String()

	assert.Contains(t, out, "swift-amber-falcon")
	assert.Contains(t, out, "Fixed the bug")
	assert.Contains(t, out, "/fake/essence.md")
	assert.Contains(t, out, "Investigate the retry backoff overflow.", "the complete essence BODY must be present, not just metadata")
}

// TestRenderSessionFullText_EmptyShowsPlaceholder mirrors
// TestRenderSessionRows_EmptyShowsPlaceholder: no rows still renders the
// friendly "(no sessions)" line rather than nothing at all.
func TestRenderSessionFullText_EmptyShowsPlaceholder(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, renderSessionFullText(&buf, nil))
	assert.Contains(t, buf.String(), "(no sessions)")
}

// newSessionFullRowForTest builds a SessionFullRow directly from given
// fields, bypassing disk I/O — used where the test cares about the renderer,
// not essence resolution (that's newSessionFullRow's job, covered above).
func newSessionFullRowForTest(harp, summary, essence, essencePath string) SessionFullRow {
	return SessionFullRow{
		SessionRow: SessionRow{
			Harp:        harp,
			Summary:     summary,
			Start:       sessionTime(time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)),
			EssencePath: essencePath,
		},
		Essence: essence,
	}
}

// TestEmitSessionRows_FullJSON_IsStructuredAndUnpaged covers the structured
// side of --full: json output must be a clean, directly-unmarshalable array
// carrying the complete essence body per row — no pager escape codes or
// text-renderer decoration mixed in (which is guaranteed structurally here
// since emitSessionRows routes json/yaml/toml straight through
// clifmt.Render, never touching pagerWriter — see the "unpaged" name).
func TestEmitSessionRows_FullJSON_IsStructuredAndUnpaged(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)

	body := "## Summary\n\nShipped the essence_path restoration.\n"
	seedDistilledEssence(t, entry.HarpName, body)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"session", "list", "--full", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		sessionListFull = false // BoolVar's Set() only runs when --full is actually parsed; reset explicitly so later tests don't inherit true
	})
	require.NoError(t, rootCmd.Execute())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output must be clean, parseable JSON: %s", out.String())
	require.Len(t, rows, 1)
	assert.Equal(t, entry.HarpName, rows[0]["harp"])
	assert.Equal(t, body, rows[0]["essence"], "the full essence body must ride in the json row")
}

// TestEmitSessionRows_FullText_SkipsPagerWhenNotTTY covers the pager guard
// end to end through the real cobra command tree: a test's captured output
// buffer is never the process's real os.Stdout, so shouldPage is false and
// output must land directly in the buffer, unmodified and without blocking
// on (or requiring) a pager process — this is what keeps `session list
// --full > file` and CI test runs working.
func TestEmitSessionRows_FullText_SkipsPagerWhenNotTTY(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)

	body := "## Summary\n\nRoot-caused the flaky retry-backoff-overflow test.\n"
	seedDistilledEssence(t, entry.HarpName, body)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"session", "list", "--full", "--format", "text"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		sessionListFull = false
	})
	require.NoError(t, rootCmd.Execute())

	out2 := out.String()
	assert.Contains(t, out2, entry.HarpName)
	assert.Contains(t, out2, "Root-caused the flaky retry-backoff-overflow test.", "the real essence body must be present")
	assert.NotContains(t, out2, "\x1b[", "no pager escape sequences should appear when the pager is skipped")
}

// TestEmitSessionRows_QueryFull_MatchesAndCarriesBody exercises `session
// query --full` end to end: the same query-filtering behavior as the
// lightweight path, but the surviving row carries the complete essence body.
func TestEmitSessionRows_QueryFull_MatchesAndCarriesBody(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	hit, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)

	body := "## Summary\n\nRoot-caused the flaky retry-backoff-overflow test.\n"
	seedDistilledEssence(t, hit.HarpName, body)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs([]string{"session", "query", "retry-backoff-overflow", "--full", "--format", "json"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
		sessionQueryFull = false // BoolVar's Set() only runs when --full is actually parsed; reset explicitly so later tests don't inherit true
	})
	require.NoError(t, rootCmd.Execute())

	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), "output: %s", out.String())
	require.Len(t, rows, 1)
	assert.Equal(t, hit.HarpName, rows[0]["harp"])
	assert.Equal(t, body, rows[0]["essence"])
}

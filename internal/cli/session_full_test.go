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
	rootCmd.SetArgs([]string{"session", "search", "retry-backoff-overflow", "--full", "--format", "json"})
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

// TestNewSessionFullRow_EssenceAndPathAgree pins the one invariant a --full
// row owes its consumer: the body and the path describe the SAME file, so a
// row can never say "here is the essence" and "this session has no essence
// file" at once. It used to bite because the row resolved the essence TWICE
// through two different appDir sources — the caller's for EssencePath, and
// readSessionEssence's own config.Load() for the body — which disagreed
// whenever the caller could not resolve one. Neither source exists now that an
// essence is addressed under its harp, so the two cannot diverge by
// construction; the invariant stays asserted because that is a claim about
// today's code, and this is what would notice a second lookup coming back.
//
// Seeded as a ROTATION essence rather than the harp's current one, so this
// covers the fallback arm as well as the primary.
func TestNewSessionFullRow_EssenceAndPathAgree(t *testing.T) {
	harp := "agree-path-and-body"
	seedRotationEssence(t, harp, "sess-9", "rotation body\n")
	e := sessions.Entry{HarpName: harp, SessionID: "sess-9"}

	row := newSessionFullRow(e, "")

	if row.Essence != "" {
		assert.NotEmpty(t, row.EssencePath,
			"a row carrying an essence BODY must also name the file it came from")
	}
	if row.EssencePath != "" {
		body, err := os.ReadFile(row.EssencePath)
		require.NoError(t, err)
		assert.Equal(t, string(body), row.Essence, "the body must be the contents of the named path")
	}
}

// emitSessionRows is an instance of the hand-rolled format
// branch that bypasses emit(). The parity check across the family's other
// hand-rolled sites (cmd/taskloom/format_test.go) found them all equivalent to
// cliemit.Emit's own predicate — except this one, which additionally routes
// MARKDOWN to the bespoke human renderer:
//
//	if format != clifmt.FormatText && format != clifmt.FormatMarkdown
//
// So one command answers `--format markdown` two different ways depending on
// --full: the non-full branch goes through emit(), which hands markdown to
// clifmt.Render, while --full renders the human text table through the pager.
// Nothing in the code says that asymmetry is intended, so it is characterized
// here rather than silently "corrected" — the decision is the command owner's.
// If the exclusion is deliberate, this test is its record; if it is not, this
// test is what goes red when it is fixed.
func TestEmitSessionRows_FullMarkdown_TakesTheHumanBranchUnlikeEmit(t *testing.T) {
	dir := testsupport.ProjectDir(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(dir, "claude-code")
	require.NoError(t, err)
	seedDistilledEssence(t, entry.HarpName, "## Summary\n\nmarkdown asymmetry.\n")

	render := func(args ...string) string {
		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&bytes.Buffer{})
		rootCmd.SetArgs(args)
		t.Cleanup(func() {
			rootCmd.SetOut(nil)
			rootCmd.SetErr(nil)
			rootCmd.SetArgs(nil)
			sessionListFull = false
		})
		require.NoError(t, rootCmd.Execute())
		return out.String()
	}

	full := render("session", "list", "--full", "--format", "markdown")
	// --full is a BoolVar: cobra only assigns it when the flag is parsed, so a
	// second invocation without it inherits true unless it is reset here.
	sessionListFull = false
	plain := render("session", "list", "--format", "markdown")

	assert.NotContains(t, full, "| HARP",
		"--full --format markdown currently renders the bespoke human view, not a clifmt markdown table")
	assert.Contains(t, plain, "|",
		"without --full the same flag goes through emit() and gets clifmt's markdown table")
}

package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// newIsolatedFlowProject isolates HOME/cwd (testsupport.ProjectDir) AND
// pre-creates a `.ctxloom` marker at the project root, then invalidates the
// ambient config memo.
//
// The extra mkdir works around a real, reproducible test-isolation hazard
// found while writing this test (see this batch's final report, "defects
// with no row" — test-isolation leak): config.findAppDir walks UP from cwd
// looking for an existing `.ctxloom` directory, with NO boundary at a
// t.TempDir() root. t.TempDir() nests under os.TempDir() (`/tmp` on this
// host), and this host's `/tmp/.ctxloom` happens to be a real, long-lived
// directory (debris from unrelated activity — dated across multiple days,
// clearly pre-existing) — so a project dir with no `.ctxloom` of its own
// silently resolves to `/tmp/.ctxloom` as if `/tmp` were the project root,
// sharing state with every other test/process on the host that hits the
// same fallback. Confirmed via a throwaway probe: cfg.GetAppPaths() ==
// ["/tmp/.ctxloom"] even with HOME correctly isolated to a fresh temp dir.
// Creating `.ctxloom` at the project root makes the walk-up find IT first,
// at depth zero, and never reach `/tmp`.
func newIsolatedFlowProject(t *testing.T) string {
	t.Helper()
	dir := testsupport.ProjectDir(t)
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755))
	config.Invalidate()
	t.Cleanup(config.Invalidate)
	return dir
}

// TestOutputFlow_TipToTail is this flow's tip-to-tail test — the gap the
// task named explicitly ("no dedicated journey — this flow's tip-to-tail
// test IS the gap"). The flow is: command result -> operations return value
// -> strictness/emit -> clifmt/JSON/TUI rendering -> user or machine
// consumer. `fragment list` is the representative command: it is fully
// wired to emit() (unlike ~36 other commands only later gated loudly), and it
// already carries the fix for the filter-typo-vs-empty-result distinction,
// so it exercises every hop this flow owns without needing a fixture from
// another flow's territory.
//
// This single test drives three scenarios the task calls out by name as
// this flow's characteristic failure modes:
//  1. success: a real result renders identically in substance across text
//     and json (no drift between the two renderers).
//  2. empty result: a bundle with zero fragments is VISIBLE ("Fragments
//     (0):" / `[]`), not zero bytes — distinguishable from a query that
//     never ran.
//  3. failure path: a bad filter is a loud error, not a silently-empty
//     "success" (re-verified here as part of this flow's own coverage
//     rather than taken on faith from the census).
func TestOutputFlow_TipToTail(t *testing.T) {
	t.Run("empty_result_is_visible_not_silent", func(t *testing.T) {
		newIsolatedFlowProject(t)
		cfg, err := config.LoadFresh()
		require.NoError(t, err)
		_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
			Name:        "empty-fixture",
			Description: "a real, known bundle with zero fragments",
			Fragments:   map[string]operations.BundleFragmentInput{},
		})
		require.NoError(t, err)

		textOut := runOutputFlowCommand(t, "text", "fragment", "list", "--bundle", "empty-fixture")
		assert.Contains(t, textOut, `No fragments in bundle "empty-fixture"`,
			"an empty result must say so explicitly, not print nothing (the project's characteristic silent-no-op) — "+
				"and must name the empty BUNDLE rather than the generic 'Fragments (0):' the unfiltered case uses")

		jsonOut := runOutputFlowCommand(t, "json", "fragment", "list", "--bundle", "empty-fixture")
		assert.JSONEq(t, "[]", jsonOut,
			"json must render an explicit empty array, not omit the payload or emit null")
	})

	t.Run("success_text_and_json_agree", func(t *testing.T) {
		newIsolatedFlowProject(t)
		cfg, err := config.LoadFresh()
		require.NoError(t, err)
		_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
			Name:        "flow-fixture",
			Description: "output-flow tip-to-tail fixture",
			Fragments: map[string]operations.BundleFragmentInput{
				"coding-standards": {Content: "use tabs", NoDistill: true},
			},
		})
		require.NoError(t, err)

		textOut := runOutputFlowCommand(t, "text", "fragment", "list", "--bundle", "flow-fixture")
		assert.Contains(t, textOut, "coding-standards", "text rendering must reflect the real operations result")
		assert.Contains(t, textOut, "flow-fixture")

		jsonOut := runOutputFlowCommand(t, "json", "fragment", "list", "--bundle", "flow-fixture")
		var rows []map[string]any
		require.NoError(t, json.Unmarshal([]byte(jsonOut), &rows))
		require.Len(t, rows, 1, "json must carry exactly the same one item text rendered")
		assert.Equal(t, "coding-standards", rows[0]["name"])
		assert.Equal(t, "flow-fixture", rows[0]["bundle"])
	})

	t.Run("bad_filter_is_a_loud_error_not_a_silent_empty_success", func(t *testing.T) {
		newIsolatedFlowProject(t)
		cfg, err := config.LoadFresh()
		require.NoError(t, err)
		_, err = operations.CreateBundle(context.Background(), cfg, operations.CreateBundleRequest{
			Name:        "real-bundle",
			Description: "so the project is non-empty when the bad filter is applied",
			Fragments: map[string]operations.BundleFragmentInput{
				"x": {Content: "hi", NoDistill: true},
			},
		})
		require.NoError(t, err)

		var out bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&out)
		rootCmd.SetArgs([]string{"fragment", "list", "--bundle", "no-such-bundle", "--format", "json"})
		t.Cleanup(func() {
			rootCmd.SetOut(nil)
			rootCmd.SetErr(nil)
			rootCmd.SetArgs(nil)
		})
		execErr := rootCmd.Execute()
		require.Error(t, execErr,
			"a --bundle filter naming NOTHING that exists must error — the caller cannot otherwise tell "+
				"'this bundle has zero fragments' apart from 'you mistyped the bundle name'")
		assert.Contains(t, execErr.Error(), "no-such-bundle")
	})
}

// runOutputFlowCommand executes rootCmd with args plus --format, requiring a
// clean exit, and returns the captured stdout.
func runOutputFlowCommand(t *testing.T, format string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append(append([]string{}, args...), "--format", format))
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	require.NoError(t, rootCmd.Execute(), "ctxloom %v --format %s", args, format)
	return out.String()
}

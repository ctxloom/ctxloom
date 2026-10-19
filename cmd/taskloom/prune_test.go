package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// pruneCmdNow is the instant the CLI prune tests measure ages from; the
// seeded events carry timestamps relative to it.
var pruneCmdNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

// seedPruneProject establishes a temp project and writes its log directly, so
// every task's last status change is a chosen age rather than the wall clock.
func seedPruneProject(t *testing.T) (operations.TaskContext, string) {
	t.Helper()
	taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)
	_, logPath := establishedLogPath(t, tc)
	at := func(days int) string {
		return pruneCmdNow.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339Nano)
	}
	raw := fmt.Sprintf(`{"op":"add","task":"old-done","text":"shipped the widget","status":"To Do","ts":%q}
{"op":"status","task":"old-done","status":"Done","ts":%q}
{"op":"add","task":"fresh-done","text":"shipped yesterday","status":"Done","ts":%q}
{"op":"add","task":"still-open","text":"open work, see old-done","status":"To Do","ts":%q}
{"op":"add","task":"old-archived","text":"dropped","status":"Archived","ts":%q}
`, at(40), at(10), at(1), at(30), at(30))
	require.NoError(t, os.MkdirAll(filepath.Dir(logPath), 0o755))
	require.NoError(t, os.WriteFile(logPath, []byte(raw), 0o644))
	return tc, logPath
}

func defaultPruneOptions(format clifmt.Format) pruneOptions {
	return pruneOptions{OlderThan: "7d", Statuses: []string{"Done"}, Format: format}
}

// pruneJSON is the --format json payload, decoded only as far as the
// assertions below read it.
type pruneJSON struct {
	Applied        bool     `json:"applied"`
	Statuses       []string `json:"statuses"`
	Before         string   `json:"before"`
	TasksPruned    int      `json:"tasks_pruned"`
	EventsRemoved  int      `json:"events_removed"`
	TasksRemaining int      `json:"tasks_remaining"`
	Pruned         []struct {
		HarpID           string `json:"harp_id"`
		Status           string `json:"status"`
		LastStatusChange string `json:"last_status_change"`
	} `json:"pruned"`
	Dangling []struct {
		HarpID string `json:"harp_id"`
		Pruned string `json:"pruned"`
		Via    string `json:"via"`
	} `json:"dangling_references"`
}

func decodePrune(t *testing.T, out string) pruneJSON {
	t.Helper()
	var got pruneJSON
	require.NoError(t, json.Unmarshal([]byte(out), &got), "--format json must be one parseable document:\n%s", out)
	return got
}

func (p pruneJSON) harps() []string {
	out := make([]string, 0, len(p.Pruned))
	for _, row := range p.Pruned {
		out = append(out, row.HarpID)
	}
	return out
}

func TestRunPruneCmd_JSONCarriesTheSelectedHarpsAndTheCountsAndChangesNothing(t *testing.T) {
	tc, logPath := seedPruneProject(t)
	before, err := os.ReadFile(logPath)
	require.NoError(t, err)

	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, defaultPruneOptions(clifmt.FormatJSON), pruneCmdNow))

	got := decodePrune(t, out.String())
	assert.False(t, got.Applied)
	assert.Equal(t, []string{"old-done"}, got.harps())
	assert.Equal(t, 1, got.TasksPruned)
	assert.Equal(t, 2, got.EventsRemoved)
	assert.Equal(t, 3, got.TasksRemaining)
	assert.Equal(t, []string{"Done"}, got.Statuses)
	assert.Equal(t, pruneCmdNow.Add(-7*24*time.Hour).Format(time.RFC3339), got.Before)
	require.Len(t, got.Dangling, 1, "still-open names old-done in its text")
	assert.Equal(t, "still-open", got.Dangling[0].HarpID)
	assert.Equal(t, "old-done", got.Dangling[0].Pruned)

	after, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "without --yes the log is not touched")
	assert.Contains(t, errw.String(), "taskloom removed nothing")
	assert.Contains(t, errw.String(), "taskloom prune --older-than 7d --status Done --yes",
		"the report names the command that applies exactly this plan")
}

func TestRunPruneCmd_ReportsTheBoundInUTC(t *testing.T) {
	tc, _ := seedPruneProject(t)

	local := pruneCmdNow.In(time.FixedZone("UTC-5", -5*60*60))
	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, defaultPruneOptions(clifmt.FormatJSON), local))
	assert.Equal(t, pruneCmdNow.Add(-7*24*time.Hour).Format(time.RFC3339), decodePrune(t, out.String()).Before,
		"the bound is reported in UTC, the zone every event's ts is in")
}

func TestRunPruneCmd_YesRemovesTheSelectedTasksFromTheLog(t *testing.T) {
	tc, logPath := seedPruneProject(t)
	listed := func() []string {
		res, err := operations.ListTasks(tc, operations.ListOptions{IncludeDone: true})
		require.NoError(t, err)
		var harps []string
		for _, task := range res.Tasks {
			harps = append(harps, task.HarpID)
		}
		return harps
	}
	var want []string
	for _, harp := range listed() {
		if harp != "old-done" {
			want = append(want, harp)
		}
	}

	opts := defaultPruneOptions(clifmt.FormatJSON)
	opts.Yes = true
	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, opts, pruneCmdNow))

	got := decodePrune(t, out.String())
	assert.True(t, got.Applied)
	assert.Equal(t, []string{"old-done"}, got.harps())
	assert.NotContains(t, errw.String(), "removed nothing")

	raw, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), `"task":"old-done"`)
	assert.Equal(t, want, listed(), "every other task is listed exactly as before, in the same order")
}

func TestRunPruneCmd_StatusClassesAndTheBoundChangeTheSelection(t *testing.T) {
	tc, _ := seedPruneProject(t)

	opts := defaultPruneOptions(clifmt.FormatJSON)
	opts.Statuses = []string{"@terminal"}
	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, opts, pruneCmdNow))
	got := decodePrune(t, out.String())
	assert.ElementsMatch(t, []string{"old-done", "old-archived"}, got.harps())
	assert.Equal(t, []string{"Done", "Archived"}, got.Statuses, "the report states the statuses the class expanded to")

	opts = defaultPruneOptions(clifmt.FormatJSON)
	opts.OlderThan = "12h"
	out.Reset()
	require.NoError(t, runPruneCmd(&out, &errw, tc, opts, pruneCmdNow))
	assert.ElementsMatch(t, []string{"old-done", "fresh-done"}, decodePrune(t, out.String()).harps())
}

func TestRunPruneCmd_TextReportSaysWhatItWouldDo(t *testing.T) {
	tc, _ := seedPruneProject(t)

	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, defaultPruneOptions(clifmt.FormatText), pruneCmdNow))

	text := out.String()
	assert.Contains(t, text, "Would prune 1 task(s), 2 event(s)")
	assert.Contains(t, text, "old-done")
	assert.Contains(t, text, "shipped the widget")
	assert.Contains(t, text, "3 task(s) remain")
	assert.Contains(t, text, "still-open -> old-done  (text)")
	assert.NotContains(t, text, "fresh-done")
}

func TestRunPruneCmd_YAMLIsHonoured(t *testing.T) {
	tc, _ := seedPruneProject(t)

	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, defaultPruneOptions(clifmt.FormatYAML), pruneCmdNow))
	assert.Contains(t, out.String(), "harp_id: old-done")
	assert.Contains(t, out.String(), "tasks_pruned: 1")
}

func TestRunPruneCmd_NothingToPruneSaysSoAndOffersNoApply(t *testing.T) {
	tc, _ := seedPruneProject(t)

	opts := defaultPruneOptions(clifmt.FormatText)
	opts.OlderThan = "90d"
	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, opts, pruneCmdNow))
	assert.Contains(t, out.String(), "Nothing to prune")
	assert.Empty(t, errw.String())
}

// With no project here yet there is nothing to prune: the report says so
// without a blank "Project:" header, the warning says why, and no identity
// is minted to answer.
func TestRunPruneCmd_NoProjectYetPrunesNothing(t *testing.T) {
	dir := taskstest.ProjectDir(t)
	tc, err := taskContextSingle()
	require.NoError(t, err)

	opts := defaultPruneOptions(clifmt.FormatText)
	opts.Yes = true
	var out, errw strings.Builder
	require.NoError(t, runPruneCmd(&out, &errw, tc, opts, pruneCmdNow))
	assert.Contains(t, out.String(), "Nothing to prune")
	assert.NotContains(t, out.String(), "Project:")
	assert.Contains(t, errw.String(), "no project yet")
	_, logPath, err := operations.ResolveLogPath(tc)
	require.NoError(t, err)
	assert.Empty(t, logPath, "a prune must not mint the project a first add would: %s", dir)
}

func TestRunPruneCmd_RefusesWhatItCannotRead(t *testing.T) {
	tc, logPath := seedPruneProject(t)
	before, err := os.ReadFile(logPath)
	require.NoError(t, err)

	for _, tc2 := range []struct {
		name string
		edit func(*pruneOptions)
		want string
	}{
		{"an age that is not one", func(o *pruneOptions) { o.OlderThan = "soon" }, "--older-than"},
		{"an unknown status class", func(o *pruneOptions) { o.Statuses = []string{"@finished"} }, "@finished"},
		{"no status at all", func(o *pruneOptions) { o.Statuses = nil }, "--status"},
		{"a malformed tag query", func(o *pruneOptions) { o.TagQuery = "release/and" }, "release/and"},
	} {
		t.Run(tc2.name, func(t *testing.T) {
			opts := defaultPruneOptions(clifmt.FormatJSON)
			opts.Yes = true
			tc2.edit(&opts)
			var out, errw strings.Builder
			err := runPruneCmd(&out, &errw, tc, opts, pruneCmdNow)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc2.want)
			after, rerr := os.ReadFile(logPath)
			require.NoError(t, rerr)
			assert.Equal(t, string(before), string(after), "a refused prune writes nothing")
		})
	}
}

// A malformed --tag-query names the postfix grammar as its remedy, exactly
// as it does for list.
func TestRunPruneCmd_MalformedTagQueryCarriesTheGrammarRemedy(t *testing.T) {
	tc, _ := seedPruneProject(t)

	opts := defaultPruneOptions(clifmt.FormatJSON)
	opts.TagQuery = "release/and"
	var out, errw strings.Builder
	err := runPruneCmd(&out, &errw, tc, opts, pruneCmdNow)
	require.ErrorIs(t, err, tasks.ErrTagQuery)
	fix, ok := clifmt.RemedyOf(err)
	require.True(t, ok)
	assert.Equal(t, tagQueryRemedy, fix)
}

// The defaults ARE the documented behaviour: Done tasks whose last status
// change is more than seven days old, and a report rather than a rewrite.
func TestPruneCmd_FlagDefaults(t *testing.T) {
	assert.Equal(t, "7d", pruneCmd.Flags().Lookup("older-than").DefValue)
	assert.Equal(t, "[Done]", pruneCmd.Flags().Lookup("status").DefValue)
	assert.Equal(t, "false", pruneCmd.Flags().Lookup("yes").DefValue)
	assert.Equal(t, "", pruneCmd.Flags().Lookup("tag-query").DefValue)
}

func TestShellWord(t *testing.T) {
	assert.Equal(t, "Done", shellWord("Done"))
	assert.Equal(t, "urgent/release/and", shellWord("urgent/release/and"))
	assert.Equal(t, "'To Do'", shellWord("To Do"))
	assert.Equal(t, "'triage:level<=2'", shellWord("triage:level<=2"))
	assert.Equal(t, `'it'\''s'`, shellWord("it's"))
}

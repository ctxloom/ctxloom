package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// addTagged is a fixture helper: one task with the given text, status and
// tags. A trigger is always supplied so a Deferred fixture is legal.
func addTagged(t *testing.T, text, status string, tags ...string) {
	t.Helper()
	_, err := operations.AddTaskWithTags(mustTaskContext(t), text, status, "some condition", tags)
	require.NoError(t, err)
}

// seedTagFixture builds a population spanning active, completed and Deferred
// tasks, with tags that overlap across those groups — so active and total
// genuinely differ per tag and a filter can be seen to move the numbers.
func seedTagFixture(t *testing.T) {
	t.Helper()
	addTagged(t, "write the release notes", "", "release", "docs")
	addTagged(t, "cut the release branch", tasks.StatusInProgress, "release")
	addTagged(t, "old release chore", tasks.StatusDone, "release")
	addTagged(t, "parked docs rewrite", tasks.StatusDeferred, "docs")
}

// tagsJSON drives runTagsCmd in json and decodes the counts.
func tagsJSON(t *testing.T, opts listOptions) []operations.TagCount {
	t.Helper()
	var stdout, stderr strings.Builder
	opts.Format = clifmt.FormatJSON
	require.NoError(t, runTagsCmd(&stdout, &stderr, mustTaskContext(t), opts))
	var got []operations.TagCount
	require.NoError(t, json.Unmarshal([]byte(stdout.String()), &got), "output was %q", stdout.String())
	return got
}

// TestRunTagsCmd_NoFlagsMatchesTheWholeProjectCounts is icy-record's
// compatibility gate: with no filter or scope flags, `taskloom tags` counts
// exactly what it always did — the whole project, completed and Deferred
// tasks included — and not the default active-only listing the new filter
// machinery would otherwise impose on it.
func TestRunTagsCmd_NoFlagsMatchesTheWholeProjectCounts(t *testing.T) {
	taskstest.ProjectDir(t)
	seedTagFixture(t)

	before, err := operations.ListTagCounts(mustTaskContext(t))
	require.NoError(t, err)

	got := tagsJSON(t, listOptions{})
	require.Equal(t, before.Tags, got, "an unfiltered `tags` must count the whole project, exactly as it did before it gained filters")

	// And the counts are the ones the fixture implies: a Done task and a
	// Deferred task each raise total without raising active.
	byTag := map[string]operations.TagCount{}
	for _, c := range got {
		byTag[c.Tag] = c
	}
	require.Equal(t, operations.TagCount{Tag: "release", Active: 2, Total: 3}, byTag["release"])
	require.Equal(t, operations.TagCount{Tag: "docs", Active: 1, Total: 2}, byTag["docs"])
}

// TestRunTagsCmd_NoFlagsTextIsTheSameTable pins the human view's shape: the
// project header, then one padded, labeled row per tag.
func TestRunTagsCmd_NoFlagsTextIsTheSameTable(t *testing.T) {
	taskstest.ProjectDir(t)
	seedTagFixture(t)

	var stdout, stderr strings.Builder
	require.NoError(t, runTagsCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Format: clifmt.FormatText}))
	out := stdout.String()

	assert.True(t, strings.HasPrefix(out, "Project: "), "the table still names its store first: %q", out)
	assert.Contains(t, out, "docs       1 active    2 total")
	assert.Contains(t, out, "release    2 active    3 total")
}

// TestRunTagsCmd_TagQueryNarrowsThePopulation is the capability itself:
// counts computed over a filtered task set rather than the whole project.
// The audit caught this being scraped — tag-query, then grep -oE, then sort
// | uniq -c — for want of one call.
func TestRunTagsCmd_TagQueryNarrowsThePopulation(t *testing.T) {
	taskstest.ProjectDir(t)
	seedTagFixture(t)

	got := tagsJSON(t, listOptions{TagQuery: "docs"})

	byTag := map[string]operations.TagCount{}
	for _, c := range got {
		byTag[c.Tag] = c
	}
	// Only the two docs-tagged tasks are counted: one active, one Deferred.
	require.Equal(t, operations.TagCount{Tag: "docs", Active: 1, Total: 2}, byTag["docs"])
	// "release" is now counted only where it co-occurs with docs — one task,
	// active — instead of the three the whole project carries.
	require.Equal(t, operations.TagCount{Tag: "release", Active: 1, Total: 1}, byTag["release"])
}

// TestRunTagsCmd_StatusFilterAndClassesNarrowThePopulation pins that `tags`
// honors --status, @-classes included: uneven-faction's classes must work
// anywhere --status appears, and this is the second place it appears.
func TestRunTagsCmd_StatusFilterAndClassesNarrowThePopulation(t *testing.T) {
	taskstest.ProjectDir(t)
	seedTagFixture(t)

	byTag := func(got []operations.TagCount) map[string]operations.TagCount {
		m := map[string]operations.TagCount{}
		for _, c := range got {
			m[c.Tag] = c
		}
		return m
	}

	t.Run("literal status", func(t *testing.T) {
		got := byTag(tagsJSON(t, listOptions{Statuses: []string{tasks.StatusDone}}))
		require.Equal(t, operations.TagCount{Tag: "release", Active: 0, Total: 1}, got["release"])
		require.NotContains(t, got, "docs", "no Done task carries docs")
	})

	t.Run("@terminal class", func(t *testing.T) {
		got := byTag(tagsJSON(t, listOptions{Statuses: []string{tasks.StatusClassTerminal}}))
		require.Equal(t, operations.TagCount{Tag: "release", Active: 0, Total: 1}, got["release"])
	})

	t.Run("@open class", func(t *testing.T) {
		got := byTag(tagsJSON(t, listOptions{Statuses: []string{tasks.StatusClassOpen}}))
		// Everything but the one Done task: release loses its completed
		// member, docs keeps both (one active, one Deferred).
		require.Equal(t, operations.TagCount{Tag: "release", Active: 2, Total: 2}, got["release"])
		require.Equal(t, operations.TagCount{Tag: "docs", Active: 1, Total: 2}, got["docs"])
	})

	t.Run("unknown class fails loud", func(t *testing.T) {
		var stdout, stderr strings.Builder
		err := runTagsCmd(&stdout, &stderr, mustTaskContext(t), listOptions{
			Statuses: []string{"@finished"},
			Format:   clifmt.FormatText,
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "@finished")
		assert.Empty(t, stdout.String())
	})
}

// TestRunTagsCmd_TermFiltersTasksNotTagNames resolves the ambiguity the task
// flagged: --term narrows which TASKS are counted, by their text. It never
// filters the tag names listed — a matching task's tags all appear, whatever
// they are called, and a tag whose NAME contains the term is absent unless
// some matching task actually carries it.
func TestRunTagsCmd_TermFiltersTasksNotTagNames(t *testing.T) {
	taskstest.ProjectDir(t)
	addTagged(t, "rewrite the onboarding guide", "", "docs", "onboarding")
	addTagged(t, "unrelated chore", "", "docs-tooling")

	got := tagsJSON(t, listOptions{Term: "onboarding"})

	names := make([]string, 0, len(got))
	for _, c := range got {
		names = append(names, c.Tag)
	}
	assert.ElementsMatch(t, []string{"docs", "onboarding"}, names,
		"every tag of the matching TASK is listed; a tag is never selected or rejected by its own name matching --term")
	assert.NotContains(t, names, "docs-tooling", "the non-matching task contributes nothing, however its tags are spelled")
}

// TestRunTagsCmd_GlobalAggregatesAcrossProjects pins the scope axis: --global
// counts every privately-homed project's tags in one table, and says so.
func TestRunTagsCmd_GlobalAggregatesAcrossProjects(t *testing.T) {
	taskstest.ProjectDir(t)
	addTagged(t, "here's task", "", "shared", "here")
	_, err := operations.AddTaskWithTags(operations.TaskContext{ProjectID: "elsewhere"}, "elsewhere's task", "", "", []string{"shared", "there"})
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	require.NoError(t, runTagsCmd(&stdout, &stderr, mustTaskContext(t), listOptions{Global: true, Format: clifmt.FormatText}))
	assert.Contains(t, stdout.String(), "Projects: 2 (--global)")

	got := tagsJSON(t, listOptions{Global: true})
	byTag := map[string]operations.TagCount{}
	for _, c := range got {
		byTag[c.Tag] = c
	}
	require.Equal(t, operations.TagCount{Tag: "shared", Active: 2, Total: 2}, byTag["shared"], "a tag used in two projects is counted across both")
	require.Contains(t, byTag, "here")
	require.Contains(t, byTag, "there")

	// Scoped to the current project it is one project and one member.
	scoped := tagsJSON(t, listOptions{})
	byTag = map[string]operations.TagCount{}
	for _, c := range scoped {
		byTag[c.Tag] = c
	}
	require.Equal(t, operations.TagCount{Tag: "shared", Active: 1, Total: 1}, byTag["shared"])
	require.NotContains(t, byTag, "there")
}

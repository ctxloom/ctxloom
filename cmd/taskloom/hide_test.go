package main

import (
	"strings"
	"testing"

	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// TestHideConfigFor_DefaultConfigHidesOnlyTagmaMetaFamily pins the
// behavior-preserving default this feature must not disturb: with no
// tag_schema resolved at all (tc.TagSchema == nil, the pre-this-feature
// status quo), hideConfigFor still reconciles to tagma's own implicit
// default (tagma.hide:"tagma:*"=true) — hiding only the tagma.* meta-family
// taskloom never stores tags in — while an ordinary task tag stays visible.
func TestHideConfigFor_DefaultConfigHidesOnlyTagmaMetaFamily(t *testing.T) {
	cfg := hideConfigFor(operations.TaskContext{})

	urgent, err := tagma.ParseTag("urgent")
	require.NoError(t, err)
	assert.False(t, tagma.TagHidden(urgent, cfg), "an ordinary tag must stay visible by default")

	meta, err := tagma.ParseTag("tagma.arity:whatever=scalar")
	require.NoError(t, err)
	assert.True(t, tagma.TagHidden(meta, cfg), "tagma's own implicit tagma:* default must still be in effect")
}

// TestHideConfigFor_ConfiguredHideNarrowsVisibility pins the config-driven
// case this feature adds: a project's tag_schema can declare an additional
// tagma.hide:<target>=true, and hideConfigFor's resulting HideConfig hides
// exactly that target, on top of (not instead of) tagma's own default.
func TestHideConfigFor_ConfiguredHideNarrowsVisibility(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)

	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	cwe, err := tagma.ParseTag("triage:cwe=79")
	require.NoError(t, err)
	assert.True(t, tagma.TagHidden(cwe, cfg))

	other, err := tagma.ParseTag("triage:type=security")
	require.NoError(t, err)
	assert.False(t, tagma.TagHidden(other, cfg))
}

// TestVisibleTags_FiltersHiddenTargetKeepsOthers pins visibleTags' shape:
// given a mixed tag list, only the tag(s) matching an active hide pattern
// are dropped — everything else survives in its original order.
func TestVisibleTags_FiltersHiddenTargetKeepsOthers(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	in := []string{"urgent", "triage:cwe=79", "release"}
	out := visibleTags(in, cfg)
	assert.Equal(t, []string{"urgent", "release"}, out)
}

// TestVisibleTags_LenientOnUnparseableTag pins the never-crash-on-a-legacy-
// tag posture: a tag string tagma.ParseTag rejects is shown as-is rather
// than dropped or erroring the whole display.
func TestVisibleTags_LenientOnUnparseableTag(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	// An embedded, unquoted ':' with no legal namespace token before it is
	// exactly the kind of pre-tagma/free-form tag ParseTag can choke on;
	// whatever the precise rejection reason, the contract is "keep it".
	legacy := "not:a:valid::tag"
	_, parseErr := tagma.ParseTag(legacy)
	require.Error(t, parseErr, "test setup: this string must actually be unparseable")

	out := visibleTags([]string{legacy, "triage:cwe=79"}, cfg)
	assert.Equal(t, []string{legacy}, out, "the unparseable tag survives; the hidden one is dropped")
}

// TestVisibleTags_EmptyInputReturnsEmpty guards the zero-tags case: no
// panic, no spurious allocation-shape surprise (nil in, nil/empty out).
func TestVisibleTags_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Empty(t, visibleTags(nil, tagma.HideConfig{}))
}

// TestVisibleTagCounts_FiltersHiddenTarget pins the `taskloom tags`
// vocabulary-enumeration filter: a TagCount whose Tag matches an active
// hide pattern is dropped from the vocabulary entirely, not just from a
// per-task tag list.
func TestVisibleTagCounts_FiltersHiddenTarget(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	in := []operations.TagCount{
		{Tag: "triage:cwe=79", Active: 1, Total: 1},
		{Tag: "urgent", Active: 2, Total: 3},
	}
	out := visibleTagCounts(in, cfg)
	require.Len(t, out, 1)
	assert.Equal(t, "urgent", out[0].Tag)
}

// TestRenderTaskTable_HidesConfiguredTagKeepsOthers is the `list` display
// site, end to end from a real HideConfig: a task carrying both a
// hide-configured tag and an ordinary one prints only the ordinary one.
func TestRenderTaskTable_HidesConfiguredTagKeepsOthers(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	list := []tasks.Task{
		{HarpID: "aaa-bbb", Status: "To Do", Text: "fix it", Tags: []string{"triage:cwe=79", "urgent"}},
	}
	var b strings.Builder
	require.NoError(t, renderTaskTable(&b, list, cfg))
	out := b.String()
	assert.Contains(t, out, "urgent")
	assert.NotContains(t, out, "triage:cwe")
}

// TestRenderTaskDetail_HidesConfiguredTagKeepsOthers is the `show` display
// site's equivalent of TestRenderTaskTable_HidesConfiguredTagKeepsOthers.
func TestRenderTaskDetail_HidesConfiguredTagKeepsOthers(t *testing.T) {
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	cfg := hideConfigFor(operations.TaskContext{TagSchema: schema})

	task := tasks.Task{HarpID: "aaa-bbb", Status: "To Do", Tags: []string{"triage:cwe=79", "urgent"}}
	var b strings.Builder
	require.NoError(t, renderTaskDetail(&b, task, cfg))
	out := b.String()
	assert.Contains(t, out, "tags: urgent")
	assert.NotContains(t, out, "triage:cwe")
}

// TestRunListCmd_HidesConfiguredTagButShowsOthers exercises `taskloom list`
// end to end against a real store: a project whose tag_schema declares
// tagma.hide:"triage:cwe"=true must still print a task's other tags while
// omitting triage:cwe from the printed line.
func TestRunListCmd_HidesConfiguredTagButShowsOthers(t *testing.T) {
	taskstest.ProjectDir(t)

	tc := mustTaskContext(t)
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	tc.TagSchema = schema

	_, err = operations.AddTaskWithTags(tc, "vulnerable thing", "", "", []string{"triage:cwe=79", "urgent"})
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, tc, listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	out := stdout.String()
	assert.Contains(t, out, "urgent")
	assert.NotContains(t, out, "triage:cwe")
}

// TestRunListCmd_DefaultConfigStillShowsNormalTags is the regression guard
// for "behavior unchanged by default": with no tag_schema hide declaration
// at all, a normal task tag still renders in `taskloom list` exactly as it
// did before this feature existed.
func TestRunListCmd_DefaultConfigStillShowsNormalTags(t *testing.T) {
	taskstest.ProjectDir(t)

	tc := mustTaskContext(t)
	_, err := operations.AddTaskWithTags(tc, "ordinary task", "", "", []string{"urgent", "release"})
	require.NoError(t, err)

	var stdout, stderr strings.Builder
	err = runListCmd(&stdout, &stderr, tc, listOptions{Format: clifmt.FormatText})
	require.NoError(t, err)

	out := stdout.String()
	assert.Contains(t, out, "urgent")
	assert.Contains(t, out, "release")
}

// TestListTagCounts_VisibleTagCountsHidesConfiguredTagFromVocabulary is the
// `taskloom tags` vocabulary command's equivalent: composing the real
// operations.ListTagCounts result through visibleTagCounts drops the
// hide-configured tag from the enumeration entirely, the same filter the
// command's own RunE applies before printing.
func TestListTagCounts_VisibleTagCountsHidesConfiguredTagFromVocabulary(t *testing.T) {
	taskstest.ProjectDir(t)

	tc := mustTaskContext(t)
	schema, err := tagschema.Parse([]string{`tagma.hide:"triage:cwe"=true`})
	require.NoError(t, err)
	tc.TagSchema = schema

	_, err = operations.AddTaskWithTags(tc, "vulnerable thing", "", "", []string{"triage:cwe=79", "urgent"})
	require.NoError(t, err)

	res, err := operations.ListTagCounts(tc)
	require.NoError(t, err)

	visible := visibleTagCounts(res.Tags, hideConfigFor(tc))
	var tagNames []string
	for _, c := range visible {
		tagNames = append(tagNames, c.Tag)
	}
	assert.Contains(t, tagNames, "urgent")
	assert.NotContains(t, tagNames, "triage:cwe=79")
}

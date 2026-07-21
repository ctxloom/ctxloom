package main

import (
	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/operations"
)

// hideConfigFor builds a tagma.HideConfig from tc's resolved tag-schema
// (its tagma.hide:<target>=<bool> declarations, tagschema.Schema.HideFacts)
// via tagma.HideConfigFromPatterns — the display-time hide config every
// listing site (renderTaskTable, renderTaskDetail, `taskloom tags`) filters
// a task's tags through before printing them.
//
// tc.TagSchema == nil (no config resolved at all) passes an empty fact set;
// tagma.HideConfigFromPatterns still seeds its own implicit default from
// that (tagma.hide:"tagma:*"=true — see tagma's resolveHidePatterns, shared
// by HideConfigFromTags/HideConfigFromPatterns/Index.HideConfig alike), so
// taskloom's own tagma.* meta-namespace stays hidden with zero config. No
// other tag is ever hidden unless a project's tag_schema says so — this is
// purely additive over today's "show every tag" behavior.
func hideConfigFor(tc operations.TaskContext) tagma.HideConfig {
	return tagma.HideConfigFromPatterns(tc.TagSchema.HideFacts())
}

// visibleTags filters tagStrings down to those NOT hidden under cfg, for a
// display site to print/join instead of the task's raw Tags. Storage is
// never touched — this only ever affects what a listing prints.
//
// A tag string tagma.ParseTag can't parse (a legacy/free-form tag predating
// tagma adoption, or simply malformed) is always kept, unfiltered: display
// must never crash on an unparseable tag, and hiding is only ever a
// judgment tagma can make about a tag it can actually parse — an
// unparseable one is lenient-shown as-is, same posture as the rest of this
// codebase's tagma adoption (never let a foreign/legacy tag break display).
func visibleTags(tagStrings []string, cfg tagma.HideConfig) []string {
	if len(tagStrings) == 0 {
		return tagStrings
	}
	out := make([]string, 0, len(tagStrings))
	for _, s := range tagStrings {
		tag, err := tagma.ParseTag(s)
		if err != nil {
			out = append(out, s)
			continue
		}
		if tagma.TagHidden(tag, cfg) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// visibleTagCounts filters counts down to those whose Tag is NOT hidden
// under cfg — the same display-hide semantics visibleTags applies to a
// task's flat tag strings, for `taskloom tags`' per-tag counts instead. A
// Tag string ParseTag can't parse is kept, same lenient posture as
// visibleTags: the vocabulary enumeration must never drop a legacy tag it
// can't reason about.
func visibleTagCounts(counts []operations.TagCount, cfg tagma.HideConfig) []operations.TagCount {
	if len(counts) == 0 {
		return counts
	}
	out := make([]operations.TagCount, 0, len(counts))
	for _, c := range counts {
		if tag, err := tagma.ParseTag(c.Tag); err == nil && tagma.TagHidden(tag, cfg) {
			continue
		}
		out = append(out, c)
	}
	return out
}

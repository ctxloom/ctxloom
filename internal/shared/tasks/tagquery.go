// This file is the tagma query-engine adapter: everything that translates
// this package's stored tag strings and a caller's postfix tag query into a
// tagma.Index and back. It sits beside type_comparator.go, which supplies the
// same adapter's client-loadable type comparison, and deliberately apart from
// task.go, which owns the Task value type and the status taxonomy — those are
// the domain, this is one engine binding for it.

package tasks

import (
	"errors"
	"fmt"
	"strings"

	tagma "github.com/benjaminabbitt/tagma/ports/go"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// ErrTagQuery marks an error as originating from a malformed --tag-query
// (tagma's Compile/QueryPostfix), letting a caller distinguish "the query
// itself is bad" from an unrelated failure (store I/O, project resolution)
// without depending on tagma's own error shape — tagma reports a parse
// failure as a plain error, not a distinct type, unlike the retired
// pkg/tagquery.ParseError. cmd/taskloom's wrapTagQueryError uses this via
// errors.Is to decide whether to append its postfix-grammar usage hint.
var ErrTagQuery = errors.New("tag query")

// filterTasks applies status/term/tag-query filters to a task list. An empty
// tagQuery applies no tag filter — the tagma index is never even built in
// that case. A non-empty tagQuery is evaluated by building a *tagma.Index
// over the CANDIDATES that already passed the status/term filter (not the
// full task list) and running QueryPostfix against it once. A malformed
// tagQuery is returned as an error — never silently degraded to an empty or
// unfiltered result (see CLAUDE.md fail-loud philosophy).
//
// Scoping the index to the status/term-passing candidates — rather than
// building it over every task — matters specifically for `not`: tagma's
// "not" complements over the index's *participating* universe (every item
// that was given at least one query-visible tag via AddItem), not over some
// wider notion of "everything". Building the index over all tasks would let
// `not urgent` return every task minus the urgent ones, including tasks a
// status/term filter should have excluded. Scoping to candidates first
// makes `not urgent` mean exactly what the old per-task
// Expr.Matches(taskHasTag(t)) loop meant: "true for every candidate that
// doesn't carry the urgent tag".
//
// schema — the project's resolved tag-schema — feeds the index's
// client-loadable type comparison (tagma SPEC.md §9): registerTypes always
// registers this package's semver comparator, and typeConfigTags bridges
// schema's declared tagma.type:<target>=<name> facts into the index as a
// synthetic config item (see typeConfigItemID), exactly the way presenceTag
// bridges the candidate-participation marker. schema == nil (every caller
// that predates this feature) contributes no type config, so relational
// operators on an undeclared target keep matching tagma's own numeric
// grammar unchanged.
func filterTasks(all []Task, statuses []string, term, tagQuery string, schema *tagschema.Schema) ([]Task, error) {
	var statusSet map[string]bool
	if len(statuses) > 0 {
		statusSet = make(map[string]bool, len(statuses))
		for _, s := range statuses {
			statusSet[s] = true
		}
	}
	termLower := strings.ToLower(strings.TrimSpace(term))

	candidates := make([]Task, 0, len(all))
	for _, t := range all {
		if statusSet != nil && !statusSet[t.Status] {
			continue
		}
		if termLower != "" && !strings.Contains(strings.ToLower(t.Text), termLower) {
			continue
		}
		candidates = append(candidates, t)
	}

	if strings.TrimSpace(tagQuery) == "" {
		return candidates, nil
	}

	idx := tagma.NewIndex()
	registerTypes(idx)
	if cfgTags := typeConfigTags(schema); len(cfgTags) > 0 {
		// A synthetic config item, exactly like presenceTag below: bridges
		// schema's tagma.type:<target>=<name> declarations into the index so
		// tagma's own query-time typeConfig scan (SPEC.md §9) finds them.
		// typeConfigItemID can never collide with a real t.HarpID (a harp is
		// always a plain hyphenated word pair; this id carries a reserved
		// namespaced colon), so it never leaks into a query result.
		idx.AddItem(typeConfigItemID, cfgTags)
	}
	for _, t := range candidates {
		// AddItem only records an id in the index if it's given at least one
		// tag — an item that never appears in idx.items doesn't participate
		// in the universe QueryPostfix's `not` complements against (see
		// tagma's Index.participatingIDs). A candidate with zero (or zero
		// still-parseable, see tagsToTagmaTags) real tags would then be
		// silently dropped from `not X` results even though the old engine
		// included it (an untagged task has no tag named X, so `not X`
		// matched it). presenceTag guarantees every candidate always
		// contributes at least one tag, so every candidate always
		// participates — matching the old per-task semantics exactly. Its
		// reserved namespace never collides with a real bare-token tag (a
		// bare tag always parses with a nil namespace), so it can never
		// match a real query atom.
		idx.AddItem(t.HarpID, append(tagsToTagmaTags(t.Tags), presenceTag()))
	}
	ids, err := idx.QueryPostfix(tagQuery)
	if err != nil {
		return nil, fmt.Errorf("%w %q: %w", ErrTagQuery, tagQuery, err)
	}
	matched := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		matched[id] = struct{}{}
	}

	// Preserve input order: today's per-task loop appends in candidate
	// order, so iterate candidates and keep the ones tagma matched, rather
	// than trusting QueryPostfix's (sorted-by-id) return order.
	out := make([]Task, 0, len(candidates))
	for _, t := range candidates {
		if _, ok := matched[t.HarpID]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// presenceNamespace is a reserved tagma namespace used only by
// presenceTag, never by a real stored tag: every stored tag comes from
// normalizeTags/ValidateTag, which only ever accept plain bare tokens —
// tagma.ParseTag on a bare token always yields a nil Namespace, so a tag
// carrying this (or any) explicit namespace can never collide with one a
// user wrote.
const presenceNamespace = "taskloom.internal"

// typeConfigItemID is the reserved synthetic item id typeConfigTags' config
// tags are indexed under (see filterTasks) — never a real t.HarpID (a harp
// is always a plain lowercase hyphenated word pair; this id's colon can
// never occur in one), so it never participates in a query's matched-id set
// even though it briefly enters idx.items.
const typeConfigItemID = "taskloom.internal:type-config"

// presenceTag is an inert marker tag added alongside every candidate's real
// tags in filterTasks — see that function's doc for why. It never matches
// a real query atom: taskloom's tag queries are always plain bare tokens
// (see this package's own tests), and a bare query atom only matches tags
// with a nil namespace (tagma's nsMatches), never a namespaced one like
// this.
func presenceTag() tagma.Tag {
	ns := presenceNamespace
	return tagma.Tag{Namespace: &ns, Key: "candidate"}
}

// tagsToTagmaTags converts a task's stored flat tag strings into tagma.Tag
// values for indexing. Stored tags are always plain bare tokens under
// today's write-time guard (see operations.validateTag), but the reader
// stays lenient: an existing log may still carry a tag written before that
// guard existed, so a tag that fails tagma.ParseTag here is skipped rather
// than failing the whole query — mirroring the old engine, whose
// taskHasTag predicate was a plain string scan that never errored on tag
// shape either.
func tagsToTagmaTags(tags []string) []tagma.Tag {
	out := make([]tagma.Tag, 0, len(tags))
	for _, s := range tags {
		tag, err := tagma.ParseTag(s)
		if err != nil {
			continue
		}
		out = append(out, tag)
	}
	return out
}

package tasks

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

func taskWithTags(harpID string, tags ...string) Task {
	return Task{HarpID: harpID, Text: harpID, Status: StatusToDo, Tags: tags}
}

// filterTasks is the tag-query engine every list surface (Store.List,
// operations.ListTasks*, the CLI, and MCP task_list) ultimately runs
// through. These cases pin the postfix grammar wiring (and, or, not,
// implicit-AND) plus the fail-loud contract on a malformed query.
func TestFilterTasksTagQuery(t *testing.T) {
	all := []Task{
		taskWithTags("urgent-release", "urgent", "release"),
		taskWithTags("urgent-only", "urgent"),
		taskWithTags("release-only", "release"),
		taskWithTags("untagged"),
	}

	cases := []struct {
		name    string
		query   string
		wantIDs []string
	}{
		{"and", "urgent/release/and", []string{"urgent-release"}},
		{"or", "urgent/release/or", []string{"urgent-release", "urgent-only", "release-only"}},
		{"not", "urgent/not", []string{"release-only", "untagged"}},
		{"implicit and", "urgent/release", []string{"urgent-release"}},
		{"empty is no filter", "", []string{"urgent-release", "urgent-only", "release-only", "untagged"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := filterTasks(all, nil, "", c.query, nil)
			if err != nil {
				t.Fatalf("filterTasks(%q): %v", c.query, err)
			}
			var ids []string
			for _, task := range got {
				ids = append(ids, task.HarpID)
			}
			if len(ids) != len(c.wantIDs) {
				t.Fatalf("query %q: got %v, want %v", c.query, ids, c.wantIDs)
			}
			want := map[string]bool{}
			for _, id := range c.wantIDs {
				want[id] = true
			}
			for _, id := range ids {
				if !want[id] {
					t.Errorf("query %q: unexpected id %q in %v", c.query, id, ids)
				}
			}
		})
	}
}

// A malformed tag query must fail loud (a user-facing error), never degrade
// to a silent empty or unfiltered result.
func TestFilterTasksMalformedTagQueryErrors(t *testing.T) {
	all := []Task{taskWithTags("a", "urgent")}
	_, err := filterTasks(all, nil, "", "and", nil)
	if err == nil {
		t.Fatal("expected an error for a malformed tag query (bare operator, arity underflow)")
	}
}

// A malformed --tag-query surfaces wrapped in ErrTagQuery, so a caller
// (cmd/taskloom's wrapTagQueryError) can distinguish "the query itself is
// bad" from an unrelated failure via errors.Is, without depending on
// tagma's own error shape (a plain error, not a distinct type).
func TestFilterTasksMalformedTagQueryWrapsErrTagQuery(t *testing.T) {
	all := []Task{taskWithTags("a", "urgent")}
	_, err := filterTasks(all, nil, "", "and", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ErrTagQuery) {
		t.Fatalf("expected err to wrap ErrTagQuery, got %v", err)
	}
}

// Operators are matched case-insensitively — a leniency tagma's grammar was
// deliberately extended with to match taskloom's pre-existing pkg/tagquery
// behavior (see tagma's SPEC.md §2). Mixed/upper-case AND/OR/NOT must
// behave identically to their lowercase forms.
func TestFilterTasksTagQueryCaseInsensitiveOperators(t *testing.T) {
	all := []Task{
		taskWithTags("urgent-release", "urgent", "release"),
		taskWithTags("urgent-only", "urgent"),
	}
	got, err := filterTasks(all, nil, "", "urgent/release/AND", nil)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	if len(got) != 1 || got[0].HarpID != "urgent-release" {
		t.Fatalf("uppercase AND = %+v, want only urgent-release", got)
	}

	got, err = filterTasks(all, nil, "", "urgent/Not", nil)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("mixed-case Not on urgent = %+v, want none (both tasks are urgent)", got)
	}
}

// filterTasks must preserve INPUT order, not tagma's internally-sorted
// return order (QueryPostfix returns ids sorted lexically). Choose harp IDs
// whose lexical order is the reverse of their input order, so a bug that
// forwarded tagma's raw result slice would be caught.
func TestFilterTasksTagQueryPreservesInputOrder(t *testing.T) {
	all := []Task{
		taskWithTags("zebra", "urgent"),
		taskWithTags("mango", "urgent"),
		taskWithTags("apple", "urgent"),
	}
	got, err := filterTasks(all, nil, "", "urgent", nil)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	wantOrder := []string{"zebra", "mango", "apple"}
	if len(got) != len(wantOrder) {
		t.Fatalf("got %d tasks, want %d", len(got), len(wantOrder))
	}
	for i, w := range wantOrder {
		if got[i].HarpID != w {
			t.Fatalf("order = %v, want %v (input order, not tagma's sorted return)", harpIDs(got), wantOrder)
		}
	}
}

// The tagma index backing a tag query must be scoped to the tasks that
// already passed the status/term pre-filter, not the whole task list —
// this is what makes `not` behave like the old per-task
// Expr.Matches(taskHasTag(t)) loop instead of complementing against a wider
// universe. A Done task carrying the queried tag must never leak into a
// `not`-query result just because it exists in `all`; it's excluded by the
// status filter before the tag query ever runs.
func TestFilterTasksTagQueryNotScopedToStatusCandidates(t *testing.T) {
	all := []Task{
		{HarpID: "keep-me", Text: "keep-me", Status: StatusToDo},
		{HarpID: "done-urgent", Text: "done-urgent", Status: StatusDone, Tags: []string{"urgent"}},
	}
	got, err := filterTasks(all, []string{StatusToDo}, "", "urgent/not", nil)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	if len(got) != 1 || got[0].HarpID != "keep-me" {
		t.Fatalf("status+not = %+v, want only keep-me (done-urgent excluded by status, not leaked into the not-universe)", got)
	}
}

// A tag written before write-time validation existed (pkg/tagquery.
// ValidateTag / operations.validateTag) may not parse under tagma's grammar
// (e.g. it contains "/"). filterTasks — the read path — must stay lenient:
// skip the unparseable tag rather than erroring the whole query, matching
// the old engine's taskHasTag, which was a plain string scan that never
// errored on tag shape.
func TestFilterTasksTagQueryLenientOnUnparseableStoredTag(t *testing.T) {
	all := []Task{
		{HarpID: "legacy", Text: "legacy", Status: StatusToDo, Tags: []string{"urgent", "legacy/malformed"}},
	}
	got, err := filterTasks(all, nil, "", "urgent", nil)
	if err != nil {
		t.Fatalf("filterTasks must not error on an unparseable stored tag: %v", err)
	}
	if len(got) != 1 || got[0].HarpID != "legacy" {
		t.Fatalf("got %+v, want the legacy task matched via its still-valid urgent tag", got)
	}
}

// blocksReleaseSemverSchema returns a *tagschema.Schema declaring
// triage:blocks-release's tagma.type=semver (tagschema.SemverTypeName) —
// the same declaration DefaultTagSchema ships, isolated here so this file's
// end-to-end query tests don't depend on internal/taskloom/config.
func blocksReleaseSemverSchema(t *testing.T) *tagschema.Schema {
	t.Helper()
	schema, err := tagschema.Parse([]string{
		`tagma.type:"triage:blocks-release"=` + tagschema.SemverTypeName,
	})
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// TestFilterTasksSemverRelationalQuery is the end-to-end proof this whole
// feature exists for: a `--tag-query`-shaped relational atom over a target
// declared tagma.type=semver orders by real SemVer 2.0.0 precedence, not
// tagma's own numeric grammar (which would reject a two-dot value like
// "0.7.0" outright, so '<='/'>=' etc. could never match it at all). Includes
// a prerelease boundary case (0.7.0-pre001 sorts strictly before the release
// 0.7.0, SemVer 2.0.0 §11.4) to prove pre-release ordering, not just
// major.minor.patch, is live through the whole index-injection path.
func TestFilterTasksSemverRelationalQuery(t *testing.T) {
	all := []Task{
		taskWithTags("below", "triage:blocks-release=0.6.9"),
		taskWithTags("prerelease-boundary", "triage:blocks-release=0.7.0-pre001"),
		taskWithTags("exact", "triage:blocks-release=0.7.0"),
		taskWithTags("above", "triage:blocks-release=0.8.0"),
		taskWithTags("untagged"),
	}
	schema := blocksReleaseSemverSchema(t)

	got, err := filterTasks(all, nil, "", `triage:blocks-release<=0.7.0`, schema)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	want := map[string]bool{"below": true, "prerelease-boundary": true, "exact": true}
	if len(got) != len(want) {
		t.Fatalf("query <=0.7.0 = %v, want exactly %v", harpIDs(got), want)
	}
	for _, task := range got {
		if !want[task.HarpID] {
			t.Errorf("query <=0.7.0: unexpected match %q", task.HarpID)
		}
	}

	// The complementary '>' query proves the boundary is exclusive the other
	// way too, and that "above" (0.8.0) is reachable at all — nothing would
	// match if the type declaration were silently ignored and the numeric
	// grammar (which can't parse "0.7.0" as a number) took over instead.
	gotAbove, err := filterTasks(all, nil, "", `triage:blocks-release>0.7.0`, schema)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	if len(gotAbove) != 1 || gotAbove[0].HarpID != "above" {
		t.Fatalf("query >0.7.0 = %v, want only [above]", harpIDs(gotAbove))
	}
}

// TestFilterTasksSemverRelationalQueryWithoutSchemaFallsBackToNumericGrammar
// pins the precedence direction the other way: with no schema (nil, every
// caller that predates this feature), tagma's own numeric grammar governs —
// and that grammar rejects a two-dot value like "0.7.0" outright, so the
// relational query matches nothing rather than silently using string or
// float comparison.
func TestFilterTasksSemverRelationalQueryWithoutSchemaFallsBackToNumericGrammar(t *testing.T) {
	all := []Task{
		taskWithTags("below", "triage:blocks-release=0.6.9"),
		taskWithTags("exact", "triage:blocks-release=0.7.0"),
	}
	got, err := filterTasks(all, nil, "", `triage:blocks-release<=0.7.0`, nil)
	if err != nil {
		t.Fatalf("filterTasks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("query <=0.7.0 with no schema = %v, want none (tagma's numeric grammar can't parse a two-dot value)", harpIDs(got))
	}
}

func harpIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = t.HarpID
	}
	return ids
}

func TestNormalizeTags(t *testing.T) {
	got := normalizeTags([]string{"beta", " alpha ", "beta", "", "  "})
	want := []string{"alpha", "beta"}
	if len(got) != len(want) {
		t.Fatalf("normalizeTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeTags = %v, want %v", got, want)
		}
	}
	if normalizeTags(nil) != nil {
		t.Fatalf("normalizeTags(nil) should stay nil, got %v", normalizeTags(nil))
	}
}

func TestUnionAndSubtractTags(t *testing.T) {
	base := normalizeTags([]string{"alpha", "beta"})

	union := unionTags(base, []string{"beta", "gamma"})
	if got := union; len(got) != 3 || got[0] != "alpha" || got[1] != "beta" || got[2] != "gamma" {
		t.Fatalf("unionTags = %v, want [alpha beta gamma]", got)
	}
	// Idempotent: unioning an already-present tag changes nothing.
	again := unionTags(union, []string{"beta"})
	if len(again) != 3 {
		t.Fatalf("unionTags idempotence: got %v", again)
	}

	sub := subtractTags(union, []string{"beta"})
	if len(sub) != 2 || sub[0] != "alpha" || sub[1] != "gamma" {
		t.Fatalf("subtractTags = %v, want [alpha gamma]", sub)
	}
	// Removing an absent tag is a no-op.
	noop := subtractTags(sub, []string{"nonexistent"})
	if len(noop) != 2 || noop[0] != "alpha" || noop[1] != "gamma" {
		t.Fatalf("subtractTags no-op = %v, want [alpha gamma]", noop)
	}
}

// Statuses is the taxonomy a client renders instead of hardcoding the status
// set: it must stay in display order and mark the terminal (Done/Archived) and
// trigger-requiring (Deferred) statuses correctly.
func TestStatuses(t *testing.T) {
	got := Statuses()
	if len(got) != len(statusTaxonomy) {
		t.Fatalf("Statuses() returned %d entries, want %d", len(got), len(statusTaxonomy))
	}
	for i, s := range got {
		if s.Name != statusTaxonomy[i].Name {
			t.Errorf("entry %d: name %q, want %q", i, s.Name, statusTaxonomy[i].Name)
		}
		if s.Order != i {
			t.Errorf("entry %d: order %d, want %d", i, s.Order, i)
		}
		wantTerminal := s.Name == StatusDone || s.Name == StatusArchived
		if s.Terminal != wantTerminal {
			t.Errorf("%s: terminal %v, want %v", s.Name, s.Terminal, wantTerminal)
		}
		wantTrigger := s.Name == StatusDeferred
		if s.RequiresTrigger != wantTrigger {
			t.Errorf("%s: requires_trigger %v, want %v", s.Name, s.RequiresTrigger, wantTrigger)
		}
	}
}

// Task's JSON shape is a cross-surface contract: `taskloom list --json`
// marshals it directly, and the taskloom MCP tools emit the same snake_case
// keys (harp_id, text, status, ...). A scripted consumer must be able to
// treat both surfaces identically.
func TestTaskMarshalsSnakeCase(t *testing.T) {
	b, err := json.Marshal(Task{
		HarpID:        "swift-amber-falcon",
		Text:          "do the thing",
		Status:        StatusToDo,
		TextHash:      "abc123def456",
		Trigger:       "v2 ships",
		OriginSession: "zesty-slack-wager",
		Tags:          []string{"release", "urgent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"harp_id", "text", "status", "checked", "text_hash", "trigger", "origin_session", "tags"} {
		if _, ok := got[key]; !ok {
			t.Errorf("marshaled Task missing %q; keys: %v", key, got)
		}
	}
}

func TestTaskMarshalOmitsEmptyOptionalFields(t *testing.T) {
	b, err := json.Marshal(Task{HarpID: "old-dill", Text: "x", Status: StatusToDo})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"trigger", "origin_session", "tags"} {
		if _, ok := got[key]; ok {
			t.Errorf("empty %q should be omitted; keys: %v", key, got)
		}
	}
}

func TestSummaryMarshalsSnakeCase(t *testing.T) {
	b, err := json.Marshal(Summary{Counts: map[string]int{StatusToDo: 1}, InProgress: []string{"old-dill"}})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["in_progress"]; !ok {
		t.Errorf("marshaled Summary missing in_progress; keys: %v", got)
	}
}

// TestFilterTasksUnparseableStoredTagIsUnmatchableAndUnannounced measures a
// consequence which the existing leniency pin
// (TestFilterTasksTagQueryLenientOnUnparseableStoredTag) does not: a stored
// tag tagma cannot parse is skipped at index time, so a query naming that
// exact tag — spelled the one way tagma's grammar allows, quoted — matches
// nothing and reports no error. The task itself stays visible via its other
// tags; it is the malformed tag, not the task, that vanishes.
//
// Whether the reader should announce it is deliberately NOT settled here. The
// reader's leniency is documented policy (tagsToTagmaTags, validateTag): a log
// written before the write-time guard existed must keep loading, and the
// designated place to surface such data is the advisory `taskloom lint` sweep.
// This test exists so the gap is measurable rather than asserted.
func TestFilterTasksUnparseableStoredTagIsUnmatchableAndUnannounced(t *testing.T) {
	all := []Task{
		{HarpID: "legacy", Text: "legacy", Status: StatusToDo, Tags: []string{"urgent", "has space"}},
		{HarpID: "other", Text: "other", Status: StatusToDo, Tags: []string{"urgent"}},
	}

	got, err := filterTasks(all, nil, "", `"has space"`, nil)
	if err != nil {
		t.Fatalf("a quoted query atom is well-formed; want no error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("the malformed stored tag is not indexed, so nothing can match it; got %+v", got)
	}

	got, err = filterTasks(all, nil, "", "urgent", nil)
	if err != nil {
		t.Fatalf("filterTasks must stay lenient: %v", err)
	}
	ids := make([]string, 0, len(got))
	for _, task := range got {
		ids = append(ids, task.HarpID)
	}
	if !slices.Contains(ids, "legacy") {
		t.Fatalf("the task must stay visible via its parseable tags; ids = %v", ids)
	}
}

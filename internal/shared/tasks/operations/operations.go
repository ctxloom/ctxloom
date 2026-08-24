// Package operations is the frontend-agnostic layer between the task store and
// its frontends (the CLI, the MCP server, and ctxloom's run integration). Per
// ADR 0019 the frontend gathers the inputs (git root, env); operations owns
// project resolution and store access.
package operations

import (
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"
	tagma "github.com/benjaminabbitt/tagma/ports/go"
	"github.com/ctxloom/ctxloom/internal/shared/tasks"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/lint"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/priority"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/projectid"
	"github.com/ctxloom/ctxloom/internal/shared/tasks/tagschema"
)

// TaskContext carries the inputs a frontend gathers for a task operation: the
// project root, the project-id (empty to resolve live from the registry), and
// the active session harp (stamped as task provenance).
type TaskContext struct {
	WorkDir     string
	ProjectID   string
	SessionHarp string

	// WorkDirIsBoundary reports whether WorkDir came from a real project
	// boundary — a CTXLOOM_ROOT override, an enclosing git repository, or an
	// in-tree .ctxloom — rather than from a bare working directory that
	// happens to be where the process started. It is the second half of the
	// SAME resolution that produced WorkDir, carried here so that a consumer
	// deciding whether a read is project-scoped never re-resolves the working
	// directory to ask (see cmd/taskloom's resolveListScope). The zero value
	// false means "not known to be a boundary", which is the conservative
	// answer for a hand-built context: it only ever widens a scope decision
	// to the explained global fallback, never narrows one silently.
	WorkDirIsBoundary bool

	// HomingMode selects where the project's task-store log lives —
	// paths.ModeHome (private, under ~/.ctxloom/tasks, keyed by a
	// minted/registered project-id) or paths.ModeRepo (checked into the
	// project tree at WorkDir/.taskloom/tasks.jsonl, no project-id involved).
	// The zero value "" behaves exactly like paths.ModeHome — today's sole
	// behavior — so every caller that predates homing-mode selection (every
	// existing internal/cli, internal/operations, and internal/lm/isolation
	// call site) keeps working completely unchanged. cmd/taskloom's own
	// frontend resolves this explicitly via internal/taskloom/config.
	// ResolveMode, which resolves to the SAME paths.ModeHome default when
	// neither a config file nor a flag decides it — see that package's doc
	// for why ModeHome (the pre-homing status quo) is the one safe silent
	// default, while ModeRepo is only ever chosen explicitly.
	HomingMode paths.Mode

	// TagSchema is the project's parsed tag-schema (see
	// internal/shared/tasks/tagschema and internal/taskloom/config's
	// TagSchema field) — declares, among other things, which (namespace,
	// key) targets are arity=scalar. nil (the zero value) means "no schema
	// at all": AddTaskWithTags/TagTask skip scalar-collapse entirely in that
	// case, so every caller that predates this feature (every existing
	// internal/cli, internal/operations, and internal/lm/isolation call
	// site, none of which construct a schema) keeps behaving exactly as
	// before. Only cmd/taskloom's own frontend populates this today, via
	// internal/taskloom/config.Config.ParsedTagSchema.
	TagSchema *tagschema.Schema
}

// TaskListResult is the render-agnostic result of ListTasks.
type TaskListResult struct {
	Path    string
	Tasks   []tasks.Task
	Summary *tasks.Summary
	Warning string // project-resolution notice (move/fork); the frontend surfaces it

	// HiddenCompleted/HiddenDeferred count the tasks that matched every
	// requested filter but were dropped by the default active-only view
	// (completed = Done/Archived, i.e. Checked). Zero when includeDone or an
	// explicit status filter disabled that view. Frontends use these so a
	// filtered listing never silently truncates its answer — a user who
	// tag-queries a vocabulary they applied to now-finished work would
	// otherwise see matches vanish with no trace.
	HiddenCompleted int
	HiddenDeferred  int

	// ProjectID/ProjectDir identify the store the listing came from. In
	// multi-root workspaces (several .ctxloom trees under one repo) the
	// resolved project is genuinely ambiguous to the user — frontends show
	// these so a listing always names its source.
	ProjectID  string
	ProjectDir string // registered project root; empty when not registered

	// OmittedByLimit counts the rows a positive `limit` cut off Tasks after
	// every other filter (status/term/tag-query) and the default active-only
	// pass had already been applied. Zero when limit was 0 (no cap) or the
	// filtered result already fit within it. Unlike HiddenCompleted/
	// HiddenDeferred, this is never folded into Summary's counts — Summary is
	// computed independently, straight from the store, so include_summary
	// counts stay the FULL uncapped counts even when Tasks itself was
	// truncated (see listTasks).
	OmittedByLimit int
}

// TaskResult is the result of a single-task mutation.
type TaskResult struct {
	Path    string
	Task    tasks.Task
	Warning string

	// ProjectID/ProjectDir identify the store the mutation landed in — a
	// pinned project-id (CTXLOOM_PROJECT_ID exported by `ctxloom run`) wins
	// over the working directory, so without this a task added from another
	// project's tree lands somewhere invisible to the user.
	ProjectID  string
	ProjectDir string
}

// ResolveProjectIdentity resolves the stable project-id for workDir, minting
// and marking a fresh identity on first sight. Returns the id and any move/fork
// notice for the frontend to surface. Used by `ctxloom run` pre-launch to
// export CTXLOOM_PROJECT_ID into the session environment.
func ResolveProjectIdentity(workDir string) (projectID, warning string, err error) {
	// Both failures are wrapped with the stage that produced them, matching
	// ResolveLogPath/resolveTaskStore: "the registry would not open" and
	// "this tree's identity would not resolve" call for different operator
	// action, and a bare error leaves the caller unable to tell them apart.
	pm, err := projectid.Open("")
	if err != nil {
		return "", "", fmt.Errorf("open project registry: %w", err)
	}
	res, err := pm.Resolve(workDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve project id: %w", err)
	}
	return res.ProjectID, res.Warning, nil
}

// ResolveLogPath resolves the per-project task log path for tc — the project-id
// pinned in tc (by `ctxloom run`) or a live registry resolution — without
// opening the store. `taskloom watch` uses it to learn which file to watch, so
// the path convention stays owned here rather than reconstructed by a frontend.
//
// In ModeRepo, projectID is always returned empty: the repo IS the identity
// in that mode (see paths.RepoTasksLogPath and internal/taskloom/config's doc),
// so there is no id to mint or resolve.
func ResolveLogPath(tc TaskContext) (projectID, logPath string, err error) {
	if tc.HomingMode == paths.ModeRepo {
		logPath, err = paths.RepoTasksLogPath(tc.WorkDir)
		if err != nil {
			return "", "", fmt.Errorf("task log path: %w", err)
		}
		return "", logPath, nil
	}
	projectID = tc.ProjectID
	if projectID == "" {
		pm, perr := projectid.Open("")
		if perr != nil {
			return "", "", fmt.Errorf("open project registry: %w", perr)
		}
		res, rerr := pm.Resolve(tc.WorkDir)
		if rerr != nil {
			return "", "", fmt.Errorf("resolve project id: %w", rerr)
		}
		projectID = res.ProjectID
	}
	logPath, err = paths.HomeTasksLogPath(projectID)
	if err != nil {
		return projectID, "", fmt.Errorf("task log path: %w", err)
	}
	return projectID, logPath, nil
}

// ListOptions names what a task listing filters and returns. IncludeDone and
// IncludeSummary are independent, same-typed switches that callers set in both
// combinations, so they are named rather than positional: a transposition must
// not be able to compile. It mirrors cmd/taskloom's own listOptions.
//
// The zero value is the default listing: no filters, active-only, no summary,
// no cap.
type ListOptions struct {
	// Statuses filters to the named statuses. Naming any status is itself an
	// opt-in to completed ones, exactly as IncludeDone is.
	Statuses []string
	// Term is a case-insensitive substring filter over task text.
	Term string
	// TagQuery is a postfix (RPN) tag filter evaluated by tagma (e.g.
	// "urgent/release/and"). Empty means no tag filter. A MALFORMED query
	// surfaces as an error — never a silently empty or unfiltered result.
	TagQuery string
	// IncludeDone opts completed (Done/Archived) and Deferred tasks back into
	// a listing that hides them by default.
	IncludeDone bool
	// IncludeSummary asks the store for per-status counts alongside the rows.
	// The counts always cover every task, and are never affected by Limit.
	IncludeSummary bool
	// Limit caps the number of rows returned; 0 means no cap.
	Limit int
}

// ListTasks resolves the project's task log and returns its tasks, filtered
// and shaped by opts. opts.Limit caps the row COUNT of the result's Tasks —
// applied last, after status/term/tag-query filtering and the default
// active-only pass, so it truncates exactly what a caller would otherwise
// have seen in full. A limit <= 0 means no cap. opts.IncludeSummary's counts
// are computed by store.Summarize() straight from the store, independent of
// the listing and the limit — they always cover every task, never just the
// (possibly truncated) page returned here.
func ListTasks(tc TaskContext, opts ListOptions) (*TaskListResult, error) {
	statuses, includeDone, includeSummary, limit := opts.Statuses, opts.IncludeDone, opts.IncludeSummary, opts.Limit
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	list, err := store.ListWithTagQuery(statuses, opts.Term, opts.TagQuery, tc.TagSchema)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	// An append-only log accretes Done entries forever, so the default view
	// shows only live work. An explicit status filter is itself the opt-in (it
	// is honored verbatim by store.List), so only filter here when none was
	// given. The summary below still folds every task, so completed counts stay
	// visible. Deferred tasks are parked on a trigger and are likewise hidden
	// from the active view — surface them with `--status Deferred` (or the
	// check-triggers command), or with includeDone.
	var hiddenCompleted, hiddenDeferred int
	if !includeDone && len(statuses) == 0 {
		active := make([]tasks.Task, 0, len(list))
		for _, t := range list {
			switch {
			case t.Checked:
				hiddenCompleted++
			case t.Status == tasks.StatusDeferred:
				hiddenDeferred++
			default:
				active = append(active, t)
			}
		}
		list = active
	}
	var omittedByLimit int
	if limit > 0 && len(list) > limit {
		omittedByLimit = len(list) - limit
		list = list[:limit]
	}
	out := &TaskListResult{Path: store.Path(), Tasks: list, Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir,
		HiddenCompleted: hiddenCompleted, HiddenDeferred: hiddenDeferred, OmittedByLimit: omittedByLimit}
	if includeSummary {
		sum, err := store.Summarize()
		if err != nil {
			return nil, fmt.Errorf("summarize: %w", err)
		}
		out.Summary = &sum
	}
	return out, nil
}

// AddTask appends a task to the project log, stamping the session as origin.
// A non-empty trigger parks the task on a revive condition; required when
// status is Deferred (the store enforces the invariant for both CLI and MCP).
func AddTask(tc TaskContext, text, status, trigger string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.AddWithTrigger(text, status, trigger)
	if err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	return proj.taskResult(store, task, warning), nil
}

// AddTaskWithTags is AddTask with an initial tag set stamped on the same
// `add` event. This is a write seam: every tag is validated against
// validateTags (this file's tagma-aware grammar/reserved-word check)
// BEFORE the store is touched, so a tag that would be permanently
// unqueryable is rejected loudly instead of silently persisted. This check
// must never move onto the fold/read path: an existing log already carrying
// such a tag must keep loading without error.
//
// When tc.TagSchema declares a target arity=scalar, tags itself is
// collapsed BEFORE the store is touched: if tags carries more than one
// value for the same scalar (namespace, key), only the LAST one (in tags's
// own order) survives — there is no existing task yet to retract a value
// from, so this is pure intra-list dedup, not an untag (see scalarCollapse).
func AddTaskWithTags(tc TaskContext, text, status, trigger string, tags []string) (*TaskResult, error) {
	if err := validateTags(tags, tc.TagSchema); err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	if tc.TagSchema != nil {
		tags, _ = scalarCollapse(tc.TagSchema, nil, tags)
	}
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.AddWithTags(text, status, trigger, tags...)
	if err != nil {
		return nil, fmt.Errorf("add task: %w", err)
	}
	return proj.taskResult(store, task, warning), nil
}

// TagTask adds and/or removes tags on an existing task in one call,
// attributing the change to the acting session. At least one of add/remove
// must be non-empty. Add is applied before remove, so a tag named in both
// lists ends up removed.
//
// This is a write seam for the `add` list only: those tags are validated
// against validateTags before the store is touched (see AddTaskWithTags),
// rejecting the whole call before any event is written. `remove` is never
// validated — subtracting an already-malformed tag never creates new
// unqueryable data, and an existing log may legitimately carry one to
// remove.
//
// When tc.TagSchema declares a target arity=scalar, the add list is passed
// through scalarCollapse against the task's CURRENT tags (read via
// store.CurrentTags, before any mutation): if a surviving incoming value
// displaces an existing tag with the same (namespace, key) but a different
// value, that existing tag is explicitly untagged first (store.RemoveTags),
// then the new value is tagged (store.AddTags) — last wins, but the log
// still records BOTH events, so history is never rewritten. An identical
// value is left alone (no untag, no-op union). tc.TagSchema == nil (every
// caller that predates this feature) skips the CurrentTags read and
// collapse entirely, behaving exactly as before.
func TagTask(tc TaskContext, harpID string, add, remove []string) (*TaskResult, error) {
	if len(add) == 0 && len(remove) == 0 {
		return nil, fmt.Errorf("at least one tag to add or remove is required")
	}
	if err := validateTags(add, tc.TagSchema); err != nil {
		return nil, fmt.Errorf("add tags: %w", err)
	}
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	var task tasks.Task
	if len(add) > 0 {
		addTags := add
		var toUntag []string
		if tc.TagSchema != nil {
			existing, cerr := store.CurrentTags(harpID)
			if cerr != nil {
				return nil, fmt.Errorf("add tags: %w", cerr)
			}
			addTags, toUntag = scalarCollapse(tc.TagSchema, existing, add)
		}
		// Add BEFORE untag: the log is append-only, so this pair is
		// necessarily two separate writes, not one atomic operation. If the
		// add lands and the untag then fails, the task carries a transient
		// duplicate value on a scalar target — recoverable (lint already
		// flags it, and a later collapse fixes it). The old ordering did
		// the untag first: if the add then failed, the task's previous
		// scalar value was already gone with nothing written to replace
		// it — an unrecoverable loss.
		if len(addTags) > 0 {
			task, err = store.AddTags(harpID, addTags...)
			if err != nil {
				return nil, fmt.Errorf("add tags: %w", err)
			}
		}
		if len(toUntag) > 0 {
			task, err = store.RemoveTags(harpID, toUntag...)
			if err != nil {
				return nil, fmt.Errorf("add tags: collapse superseded scalar tag: %w", err)
			}
		}
	}
	if len(remove) > 0 {
		task, err = store.RemoveTags(harpID, remove...)
		if err != nil {
			return nil, fmt.Errorf("remove tags: %w", err)
		}
	}
	return proj.taskResult(store, task, warning), nil
}

// scalarCollapse enforces schema's arity=scalar declarations over incoming
// against a task's existingTags (nil for a brand-new task with no state
// yet): it returns toAdd — incoming with every non-last duplicate value for
// the same scalar target dropped (last wins, by incoming's own order) — and
// toUntag — any tag in existingTags whose (namespace, key) matches a
// SURVIVING incoming scalar target but whose value differs (an identical
// value is left alone: re-tagging the same value is a no-op, not a
// retraction). Every non-scalar (undeclared, or declared some other arity)
// tag passes through untouched in both directions, preserving today's
// multi-value behavior for anything not declared scalar. A malformed tag
// (already rejected by validateTag on this write path, but tolerated here
// for defense-in-depth — see tagsToTagmaTags's read-side leniency) is
// treated as non-scalar: it is never dropped or collapsed, just passed
// through as-is.
func scalarCollapse(schema *tagschema.Schema, existingTags, incoming []string) (toAdd, toUntag []string) {
	if schema == nil || len(incoming) == 0 {
		return incoming, nil
	}

	// lastIdx maps a scalar target to the index of its LAST occurrence in
	// incoming; only that occurrence survives into toAdd.
	targets := make([]string, len(incoming))
	lastIdx := map[string]int{}
	for i, raw := range incoming {
		target, scalar := scalarTargetOf(schema, raw)
		if scalar {
			targets[i] = target
			lastIdx[target] = i
		}
	}
	for i, raw := range incoming {
		if targets[i] != "" && lastIdx[targets[i]] != i {
			continue // superseded by a later value for the same scalar target
		}
		toAdd = append(toAdd, raw)
	}

	// surviving maps each scalar target that made it into toAdd to its
	// PARSED value, so existingTags can be checked for a
	// same-target-different-value tag that needs retracting. Parsed, not raw:
	// target identity is already decided by parsing, and tagma's grammar
	// admits a quoted spelling of any component, so `k=v` and `k="v"` are one
	// tag. Comparing raw strings makes the second look like a new value and
	// retracts the first -- an untag written into an append-only log for a
	// value that never changed.
	surviving := map[string]string{}
	for _, raw := range toAdd {
		if target, value, scalar := scalarTagOf(schema, raw); scalar {
			surviving[target] = value
		}
	}
	for _, existing := range existingTags {
		target, value, scalar := scalarTagOf(schema, existing)
		if !scalar {
			continue
		}
		newValue, ok := surviving[target]
		if ok && newValue != value {
			toUntag = append(toUntag, existing)
		}
	}
	return toAdd, toUntag
}

// scalarTargetOf parses raw as a tagma tag and reports its schema Target
// string and whether schema declares that target arity=scalar. Every caller
// in this file gates on the returned bool before trusting target: an
// unparseable or bare (no-namespace) tag always reports ("", false) — a
// scalar declaration is always namespaced (e.g. "triage:type"), so a bare
// flat tag can never match one — but a namespaced tag whose target simply
// isn't declared scalar reports (target, false) with a NON-empty target,
// since IsScalar(target) is what decided false, not an absent namespace.
func scalarTargetOf(schema *tagschema.Schema, raw string) (target string, scalar bool) {
	target, _, scalar = scalarTagOf(schema, raw)
	return target, scalar
}

// scalarTagOf is scalarTargetOf plus the tag's DECODED value ("" when the tag
// carries none) -- the form any value comparison must use. Two tags spelled
// differently but parsing to the same value ARE the same value: tagma decodes
// a quoted component to its canonical content, so `k=v` and `k="v"` both
// yield "v" here, while a raw-string comparison would call them different.
func scalarTagOf(schema *tagschema.Schema, raw string) (target, value string, scalar bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", false
	}
	parsed, err := tagma.ParseTag(trimmed)
	if err != nil || parsed.Namespace == nil {
		return "", "", false
	}
	if parsed.Value != nil {
		value = *parsed.Value
	}
	target = tagschema.Target(parsed)
	return target, value, schema.IsScalar(target)
}

// validateTags calls validateTag for every tag in tags, in order, against
// schema, returning the first error encountered (nil if every tag is
// queryable and, where schema declares an enum/range for its target,
// schema-conformant). This is the write-side counterpart to
// internal/shared/tasks.filterTasks's lenient read side: a tag rejected here
// can never be stored, so filterTasks's leniency (skipping a tag
// tagma.ParseTag can't parse, or one violating an enum/range it never
// checks) only ever has to cover data written before this guard existed —
// see validateTag's own doc for why schema is applied ONLY to tags, never to
// a task's pre-existing stored tags.
func validateTags(tags []string, schema *tagschema.Schema) error {
	for _, t := range tags {
		if err := validateTag(t, schema); err != nil {
			return err
		}
	}
	return nil
}

// validateTag reports whether tag would be PERMANENTLY UNQUERYABLE once
// stored, under tagma's grammar plus this package's own reserved-word rule;
// would inject config into the reserved tag-schema namespace; or — when
// schema declares an enum or range for tag's (namespace, key) target —
// carries a value schema doesn't allow. A tag is rejected when:
//
//   - trimmed and compared case-insensitively, it equals a reserved
//     operator word ("and", "or", "not"): tagma's postfix evaluator always
//     treats such a token as an operator (case-insensitively, per tagma's
//     leniency — see evalPostfix), so a tag with that exact name could
//     never appear as a queryable atom; it would always be consumed as an
//     operator instead. It stays reachable only by quoting it at query
//     time, which is a footgun this guard exists to steer users away from
//     needing.
//   - it fails tagma.ParseTag: tagma's tag grammar is a bare token
//     ([A-Za-z0-9_][A-Za-z0-9_.-]*, optionally namespaced/valued), so a tag
//     containing "/", "*", "+", whitespace, or any other character outside
//     that charset can never be written as a single matchable atom.
//   - its namespace is tagschema.FacetNamespace ("tagma") or any
//     "tagma.<facet>" sub-namespace: that namespace is reserved for
//     tag-schema DECLARATIONS in .taskloom/config.yaml's tag_schema (see
//     internal/shared/tasks/tagschema) — a task tag must never be able to
//     inject schema config that a user-facing write path (add/tag) can
//     reach, only config.yaml can.
//   - schema declares a tagma.enum:<target> for tag's target and tag's value
//     is not one of the declared members — the same closed-vocabulary check
//     internal/shared/tasks/lint.Lint already performs read-time, now also
//     enforced before the value is ever stored.
//   - schema declares a tagma.range:<target> for tag's target and tag's
//     value either does not parse as a number or falls outside the declared
//     [min,max] — same source of truth as lint's own range check.
//   - schema declares a tagma.type:<target>=semver (tagschema.SemverTypeName
//     — tagma SPEC.md §9's client-loadable type comparison) for tag's target
//     and tag's value does not parse as a strict SemVer 2.0.0 version (via
//     github.com/Masterminds/semver/v3's StrictNewVersion, the same parser
//     internal/shared/tasks's registered comparator uses for matching): a
//     value this rejects could never match one of the target's own
//     relational-operator ('>' '>=' '<' '<=') queries either, since the
//     comparator would report it NotComparable forever — rejecting it here
//     keeps a malformed value from being silently unqueryable once stored,
//     the same motivation as the enum/range checks above. A declared type
//     name this package doesn't recognize (anything other than
//     tagschema.SemverTypeName) is not checked here — nothing else is a
//     write-time-checkable format yet.
//
// schema is applied ONLY to tag itself — the value being written — never to
// any tag already stored on the task being mutated. AddTaskWithTags/TagTask
// only ever pass their own incoming add-list through validateTags; a task's
// EXISTING tags (including 318 live legacy/foreign ones predating this
// standard, or tags any other project's schema wrote) are never
// re-validated on a write that doesn't touch them, so e.g. task_set_status
// or task_edit on such a task keeps working. A nil schema (every caller that
// predates this feature, and any call site that never resolved one) skips
// the enum/range checks entirely — see tagschema.Schema's own
// nil-receiver-safety.
//
// An empty or all-whitespace tag is not rejected here (validateTag never
// panics on ""): callers that drop empty tags before storage — see
// normalizeTags in internal/shared/tasks — already handle that case, and it
// is not something this grammar makes unqueryable.
//
// This is a write-time check only. It must never run on the read/fold path:
// an existing log already containing a malformed or schema-violating tag
// must still fold and list without error (see
// internal/shared/tasks.tagsToTagmaTags), so a reader stays lenient even
// where a writer is now strict — `taskloom lint` is the read-time,
// advisory-only sweep for exactly that pre-existing data (see
// internal/shared/tasks/lint's package doc).
func validateTag(tag string, schema *tagschema.Schema) error {
	trimmed := strings.TrimSpace(tag)
	if trimmed == "" {
		return nil
	}
	switch strings.ToLower(trimmed) {
	case "and", "or", "not":
		return fmt.Errorf("tag %q is a reserved operator word in tagma's query grammar (and/or/not, matched case-insensitively) — it would always parse as an operator, never as a matchable tag, and so would be permanently unqueryable except by quoting it at query time", tag)
	}
	parsed, err := tagma.ParseTag(trimmed)
	if err != nil {
		return fmt.Errorf("tag %q is not a valid tag under tagma's grammar and would be permanently unqueryable once stored: %w", tag, err)
	}
	if parsed.Namespace != nil {
		if ns := *parsed.Namespace; ns == tagschema.FacetNamespace || strings.HasPrefix(ns, tagschema.FacetNamespace+".") {
			return fmt.Errorf("tag %q uses the %q namespace, which is reserved for tag-schema declarations in .taskloom/config.yaml's tag_schema — a task tag may not write into it", tag, ns)
		}
	}
	if parsed.Value == nil {
		return nil
	}
	target := tagschema.Target(parsed)
	value := *parsed.Value
	if enum, ok, eerr := schema.Enum(target); ok {
		if eerr != nil {
			return fmt.Errorf("tag %q: %w", tag, eerr)
		}
		if !slices.Contains(enum, value) {
			return fmt.Errorf("tag %q's value %q is not one of %s's declared enum values %v", tag, value, target, enum)
		}
	}
	if min, max, ok, rerr := schema.Range(target); ok {
		if rerr != nil {
			return fmt.Errorf("tag %q: %w", tag, rerr)
		}
		f, perr := strconv.ParseFloat(value, 64)
		// strconv.ParseFloat accepts "NaN"/"Inf"/"-Inf" case-insensitively,
		// and every `<`/`>` comparison against NaN is false — so a NaN
		// value would otherwise pass this gate silently and go
		// on to hang priority.rankNormalize. Treat non-finite
		// exactly like an unparseable value.
		if perr == nil && (math.IsNaN(f) || math.IsInf(f, 0)) {
			perr = fmt.Errorf("%q is not a finite number", value)
		}
		if perr != nil {
			return fmt.Errorf("tag %q's value %q does not parse as a number, required by %s's declared range [%v,%v]", tag, value, target, min, max)
		}
		if f < min || f > max {
			return fmt.Errorf("tag %q's value %v is outside %s's declared range [%v,%v]", tag, f, target, min, max)
		}
	}
	if typeName, ok := schema.Get(tagschema.TypeFacet, target); ok && typeName == tagschema.SemverTypeName {
		if _, verr := semver.StrictNewVersion(value); verr != nil {
			return fmt.Errorf("tag %q's value %q is not a valid SemVer 2.0.0 version, required by %s's declared type %q: %w", tag, value, target, typeName, verr)
		}
	}
	return nil
}

// SetTaskStatus moves a task to a different status, attributing the change to
// the acting session. A non-empty trigger (re)sets the revive condition;
// moving to Deferred requires one (supplied here or already on the task).
func SetTaskStatus(tc TaskContext, harpID, status, trigger string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.SetStatusWithTrigger(harpID, status, trigger)
	if err != nil {
		return nil, fmt.Errorf("set status: %w", err)
	}
	return proj.taskResult(store, task, warning), nil
}

// EditTask replaces a task's text in place, keyed by harp ID, attributing the
// edit to the acting session. The whole text is replaced; status and trigger
// are untouched.
func EditTask(tc TaskContext, harpID, text string) (*TaskResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	task, err := store.SetText(harpID, text)
	if err != nil {
		return nil, fmt.Errorf("edit task: %w", err)
	}
	return proj.taskResult(store, task, warning), nil
}

// TagCount reports one tag's usage across the project's tasks. Active counts
// the tasks visible in the default list view (not completed, not Deferred);
// Total counts every task carrying the tag, however parked or finished.
// Showing both is what makes the number self-explanatory: a tag with
// "0 active, 40 total" is a finished workstream, not a typo.
type TagCount struct {
	Tag    string `json:"tag"`
	Active int    `json:"active"`
	Total  int    `json:"total"`
}

// TagListResult is the render-agnostic result of ListTagCounts.
type TagListResult struct {
	Path    string
	Tags    []TagCount
	Warning string

	ProjectID  string
	ProjectDir string
}

// ListTagCounts enumerates the tags currently in use across ALL of the
// project's tasks (including completed and Deferred ones), with per-tag
// counts, sorted by tag name. This is the read side of the tag vocabulary:
// tags are free-form on write, so without an enumeration surface the
// vocabulary is write-only and typo-twins accumulate invisibly.
func ListTagCounts(tc TaskContext) (*TagListResult, error) {
	store, proj, warning, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	list, err := store.List(nil, "")
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return &TagListResult{Path: store.Path(), Tags: TagCountsOf(list), Warning: warning, ProjectID: proj.ID, ProjectDir: proj.Dir}, nil
}

// TagCountsOf is the tag-counting body itself, over whatever task set a
// caller hands it: ListTagCounts passes the whole project, `taskloom tags`
// passes the set its filters selected. Both count the same way — Active is
// the members visible in the default list view (not completed, not Deferred),
// Total is every member carrying the tag — so a filtered count and a
// whole-project count are the same number computed over different
// populations, never two different definitions of the word.
//
// Results are sorted by tag name, so output is diffable regardless of the
// order the tasks arrived in.
func TagCountsOf(list []tasks.Task) []TagCount {
	counts := make(map[string]*TagCount)
	for _, t := range list {
		active := !t.Checked && t.Status != tasks.StatusDeferred
		for _, tag := range t.Tags {
			c := counts[tag]
			if c == nil {
				c = &TagCount{Tag: tag}
				counts[tag] = c
			}
			c.Total++
			if active {
				c.Active++
			}
		}
	}
	out := make([]TagCount, 0, len(counts))
	for _, c := range counts {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tag < out[j].Tag })
	return out
}

// DeferredSince resolves the project's task store and returns, for every
// currently Deferred task, the timestamp it most recently entered that
// status. Trigger evaluation uses this to scope its evidence per task; it is
// otherwise the same TaskContext-resolution wrapper as ListTasks/AddTask.
func DeferredSince(tc TaskContext) (map[string]time.Time, error) {
	store, _, _, err := resolveTaskStore(tc)
	if err != nil {
		return nil, err
	}
	since, err := store.DeferredSince()
	if err != nil {
		return nil, fmt.Errorf("deferred since: %w", err)
	}
	return since, nil
}

// ComputeTaskPriorities resolves tc's task store and derives a
// priority.Result for every task currently in it (any status), rank-
// normalizing raw scores across the project's NON-TERMINAL tasks (see
// priority.Compute) against the store's FULL current snapshot — never just
// whatever term/tag-query/status page a caller's own listing already
// filtered down to. Percentile rank is a property of the whole active
// population; letting an already-narrow filtered view supply its own
// reference frame would distort it (e.g. filtering to one tag would make its
// single highest-raw-score member look artificially maximal against a
// population of one, rather than against the project's real spread).
//
// now is injected (never time.Now() internally) — production call sites
// (cmd/taskloom's `list --sort priority` and task_list's sort="priority")
// pass time.Now(); tests pin it for determinism.
//
// The returned priority.Diagnostics tells a caller when the ranking it just
// got back, however fully populated it looks, is actually MEANINGLESS (no
// priority_fn declared, or every non-terminal task tied) — see that type's
// doc. A caller driving `--sort priority` is expected to surface a degenerate
// Diagnostics to the user rather than silently rendering the numbers.
func ComputeTaskPriorities(tc TaskContext, now time.Time) (map[string]priority.Result, priority.Diagnostics, error) {
	store, _, _, err := resolveTaskStore(tc)
	if err != nil {
		return nil, priority.Diagnostics{}, err
	}
	all, err := store.Snapshot()
	if err != nil {
		return nil, priority.Diagnostics{}, fmt.Errorf("snapshot for priority computation: %w", err)
	}
	return priority.Compute(all, tc.TagSchema, now)
}

// LintTasks resolves tc's task store and reports internal/shared/tasks/
// lint.Lint's triage-standard violations across EVERY task in it, regardless
// of status — lint is read-time and advisory (see that package's doc); it
// never blocks a write and never filters what it inspects, unlike ListTasks'
// active-only default view.
func LintTasks(tc TaskContext) (lint.Result, error) {
	store, _, _, err := resolveTaskStore(tc)
	if err != nil {
		return lint.Result{}, err
	}
	all, err := store.Snapshot()
	if err != nil {
		return lint.Result{}, fmt.Errorf("snapshot for lint: %w", err)
	}
	return lint.Lint(all, tc.TagSchema)
}

// RepairStore re-introduces, under a fresh harp each, any task the log
// detected as a displaced duplicate-add (two different tasks independently
// minted with the same harp id — see tasks.Store.Repair and
// log.go's anomalyError). Idempotent; a no-op on a log with no unresolved
// collision.
//
// This is the shipped surface the fatal collision error itself names
// ("run Store.Repair()") — before this, that remedy was unreachable from
// anywhere a user could act on it: no `taskloom repair` command and no
// `task_repair` MCP tool existed, so every read (list/summary/tag-query/
// priority/lint) kept failing loud with no way to actually resolve it.
func RepairStore(tc TaskContext) error {
	store, _, _, err := resolveTaskStore(tc)
	if err != nil {
		return err
	}
	return store.Repair()
}

// projectIdentity names the project a task operation resolved to, so
// frontends can show which store they acted on — in multi-root workspaces
// (several .ctxloom trees under one repo) the resolved project is genuinely
// ambiguous to the user.
type projectIdentity struct {
	ID  string
	Dir string // registered project root; empty when not registered
}

// taskResult is the one shape every single-task mutation returns. Every field
// is populated here, so a field added to TaskResult cannot be left unset on
// some mutation paths and not others.
func (p projectIdentity) taskResult(store *tasks.Store, task tasks.Task, warning string) *TaskResult {
	return &TaskResult{
		Path:       store.Path(),
		Task:       task,
		Warning:    warning,
		ProjectID:  p.ID,
		ProjectDir: p.Dir,
	}
}

// resolveTaskStore opens the project's task log for tc: in ModeRepo, the
// checked-in log inside tc.WorkDir (resolveRepoHomedStore); otherwise
// (ModeHome, or "" — every pre-homing caller) the project-id from tc (set by
// `ctxloom run`) or a live registry resolution, then OpenLog. The
// project-resolution warning is returned for the frontend to surface; it is
// never printed here.
func resolveTaskStore(tc TaskContext) (store *tasks.Store, proj projectIdentity, warning string, err error) {
	if tc.HomingMode == paths.ModeRepo {
		return resolveRepoHomedStore(tc)
	}
	pm, pmErr := projectid.Open("")
	proj, warning, err = resolveProjectFor(tc, pm, pmErr)
	if err != nil {
		return nil, proj, "", err
	}
	logPath, err := paths.HomeTasksLogPath(proj.ID)
	if err != nil {
		return nil, proj, warning, fmt.Errorf("task log path: %w", err)
	}
	// "Resolved cleanly" and "has a task log" are different facts: a
	// project-id can resolve normally to an id whose jsonl was simply never
	// written (a fresh id adopted over a stale one, a lost/rebuilt registry,
	// ...). Opened via tasks.OpenLog below with no existence check, that
	// reads back as a plain empty project — "(no tasks)" — even when a
	// DIFFERENT id also registered at this same path already has a real
	// backlog sitting right there. Say so instead of staying silent.
	if pmErr == nil && tc.WorkDir != "" {
		warning = appendNote(warning, missingLogSiblingNote(pm, tc.WorkDir, proj.ID, logPath))
	}
	store, err = tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		return nil, proj, warning, err
	}
	return store, proj, warning, nil
}

// resolveProjectFor answers WHICH project a home-homed operation acts on: the
// id pinned in tc (exported by `ctxloom run`) or a live registry resolution,
// plus the registered root to display and any note the frontend should
// surface. pmErr is the registry's own open failure, carried in rather than
// re-derived: it is fatal only when the id has to be resolved live, and
// merely disables the advisory lookups otherwise.
func resolveProjectFor(tc TaskContext, pm *projectid.Manager, pmErr error) (proj projectIdentity, warning string, err error) {
	proj.ID = tc.ProjectID
	if proj.ID == "" {
		if pmErr != nil {
			return proj, "", fmt.Errorf("open project registry: %w", pmErr)
		}
		res, rerr := pm.Resolve(tc.WorkDir)
		if rerr != nil {
			return proj, "", fmt.Errorf("resolve project id: %w", rerr)
		}
		proj.ID = res.ProjectID
		warning = res.Warning
	}
	if pmErr != nil {
		return proj, warning, nil
	}
	// Best-effort: name the registered root for the id so frontends can show
	// where the store lives. A registry miss leaves Dir empty — never fatal.
	if e, lerr := pm.ResolveByID(proj.ID); lerr == nil && e != nil {
		proj.Dir = e.Path
	}
	return proj, appendNote(warning, pinMismatchNote(tc, pm, proj.ID)), nil
}

// pinMismatchNote reports when a pinned project-id silently wins over a
// working directory that demonstrably belongs to a DIFFERENT project — its
// own marker or its registry entry says so. This is exactly how tasks end up
// filed under the wrong project from a session that cd'd elsewhere. "" when
// there is no pin, no working directory, or no disagreement.
func pinMismatchNote(tc TaskContext, pm *projectid.Manager, resolvedID string) string {
	if tc.ProjectID == "" || tc.WorkDir == "" {
		return ""
	}
	// ReadMarker's error is the one signal that the tree's own marker was
	// REFUSED (crafted to escape the tasks directory, or unreadable) rather
	// than simply absent; the two states must not read alike. The pin still
	// wins — this is a diagnostic, not a refusal.
	note := ""
	cwdID, merr := projectid.ReadMarker(tc.WorkDir)
	if merr != nil {
		note = fmt.Sprintf(
			"acting on pinned project %s; %s carries a project marker that was refused (%v), so it could not be checked against the pin",
			resolvedID, tc.WorkDir, merr)
	}
	if cwdID == "" {
		if e, lerr := pm.ResolveByPath(tc.WorkDir); lerr == nil && e != nil {
			cwdID = e.ProjectID
		}
	}
	if cwdID == "" || cwdID == resolvedID {
		return note
	}
	return appendNote(note, fmt.Sprintf(
		"acting on pinned project %s, but %s belongs to project %s — pass --project %s to target it",
		resolvedID, tc.WorkDir, cwdID, cwdID))
}

// appendNote joins a resolution note onto the warning a frontend will
// surface, semicolon-separated. An empty note adds nothing, so a caller never
// has to guard the call; an empty warning is replaced rather than prefixed
// with a stray separator.
func appendNote(warning, note string) string {
	switch {
	case note == "":
		return warning
	case warning == "":
		return note
	default:
		return warning + "; " + note
	}
}

// missingLogSiblingNote reports a human-facing note when resolvedID's own
// task log doesn't exist yet AND workDir is also registered (in pm) under a
// DIFFERENT project-id whose log DOES exist on disk. This is the silent-
// empty-backlog case ADR 0025's mismatch warning doesn't cover: nothing here
// DISAGREES about identity (no stale pin fighting the cwd — that's
// resolveTaskStore's separate check, just above), so nothing would otherwise
// warn, and a listing under resolvedID would print an honest-looking
// "(no tasks)" while a sibling id bound to the exact same path already has
// real ones. Returns "" (nothing to say) when resolvedID's log already
// exists, workDir has no other registrations, or none of them has an
// on-disk log — the overwhelmingly common case, so this stays a no-op for
// every project that has never hit the mismatch.
func missingLogSiblingNote(pm *projectid.Manager, workDir, resolvedID, logPath string) string {
	if _, err := os.Stat(logPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "" // resolvedID's own log exists (or its state is otherwise inconclusive) -- nothing hidden.
	}
	// A failure below is NOT "nothing to report": this note exists precisely
	// so a project's real backlog cannot hide behind an honest-looking
	// "(no tasks)", and answering "nothing hidden" when the check never
	// managed to look is the one answer it must not give.
	entries, err := pm.EntriesAtPath(workDir)
	if err != nil {
		return fmt.Sprintf("project %s has no task log yet, and %s's other registrations could not be read (%v) -- another project may hold this directory's tasks",
			resolvedID, workDir, err)
	}
	for _, e := range entries {
		if e.ProjectID == resolvedID {
			continue
		}
		siblingPath, perr := paths.HomeTasksLogPath(e.ProjectID)
		if perr != nil {
			return fmt.Sprintf("project %s has no task log yet, and %s is also registered under project id %q, which has no usable log path (%v)",
				resolvedID, workDir, e.ProjectID, perr)
		}
		if _, serr := os.Stat(siblingPath); serr == nil {
			return fmt.Sprintf("project %s has no task log yet, but %s is ALSO registered here under project %s, which has one -- pass --project %s to see those tasks",
				resolvedID, workDir, e.ProjectID, e.ProjectID)
		}
	}
	return ""
}

// resolveRepoHomedStore opens the REPO-HOMED task store: the log lives
// checked into the project tree at tc.WorkDir/paths.RepoDirName/
// paths.RepoTasksFileName (.taskloom/tasks.jsonl), alongside
// .taskloom/config.yaml. The repo IS the identity in this mode — no
// project-id is minted or consulted (see paths.RepoTasksLogPath's doc and
// internal/taskloom/config's package doc for the full justification).
// tc.WorkDir is expected to already be redirected through the
// worktree-to-primary-checkout boundary by the caller (workdir.
// ResolveBoundary via projectroot.TaskStoreRoot), so a linked worktree lands
// on the SAME log a primary-checkout invocation would — exactly like
// ModeHome's project-id resolution already does today.
func resolveRepoHomedStore(tc TaskContext) (store *tasks.Store, proj projectIdentity, warning string, err error) {
	proj.Dir = tc.WorkDir
	if tc.ProjectID != "" {
		warning = fmt.Sprintf("--project %s is ignored in repo-homed mode; %s is the store's identity", tc.ProjectID, tc.WorkDir)
	}
	logPath, err := paths.RepoTasksLogPath(tc.WorkDir)
	if err != nil {
		return nil, proj, warning, fmt.Errorf("task log path: %w", err)
	}
	store, err = tasks.OpenLog(logPath, tc.SessionHarp)
	if err != nil {
		return nil, proj, warning, err
	}
	return store, proj, warning, nil
}

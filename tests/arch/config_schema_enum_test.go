//go:build arch

// resources/schema/input/config-schema.json is HAND-AUTHORED (see
// internal/config/arch_test.go's own comment: the schema, not a struct, is
// the source of truth for enums, and reflection cannot derive them). Every
// enum array in it that mirrors a closed Go vocabulary is therefore a SECOND
// COPY of that vocabulary's member list, kept in sync by nobody but a human
// remembering to touch both files. Nothing before this gate ever checked
// that the two stayed equal — config_home was the one instance a task
// happened to touch; runtime, permissions, driving, and the rest were never
// looked at.
//
// This gate closes that for every enum the schema declares, not a
// hand-picked subset. hand-picking the coverage is the exact failure mode
// being guarded against here: a coverage list that silently omits a member is
// the same defect as the enum it is meant to catch drifting. So the gate
// DISCOVERS every "enum" array in the schema by walking the parsed JSON
// (schemaEnumPaths) rather than reading them off a table, and
// TestArch_ConfigSchemaEnums_TableIsComplete asserts that discovered set is
// EXACTLY schemaEnumBindings' key set — no more, no less. Add an enum to the
// schema and forget the table, and that test names the orphaned path. Remove
// one and leave a stale table row, and it names that too.
//
// Each covered path either binds to a real Go `*Names()` (or equivalent)
// accessor — TestArch_ConfigSchemaEnums_MatchGoVocabulary then asserts the
// (sorted) schema values equal the (sorted) Go values exactly — or is
// EXCLUDED with a stated reason (excludeReason, non-empty). An excluded entry
// is not a gap this gate hides; it is one this gate NAMES, in the failure
// output of TestArch_ConfigSchemaEnums_ExclusionsAreExplained, printed
// whenever the exclusion table changes shape. Three excluded classes exist
// today: values a vendor CLI owns and ctxloom only passes through
// (kiro's `effort`/`agent_engine`, claude-code's hook `type`); the `role`
// field, which is registry-only display metadata the schema itself says is
// "stripped from persisted user configs and ignored otherwise"; and the
// escalation ladder's `kinds`/`action`, whose real vocabulary
// (internal/agentcoord/coord.approvalKindNames, coord.LadderAction) is
// unexported in a package this test cannot reach without either a production
// export change (out of scope for a test-only gate) or an import cycle
// (coord depends on internal/config, which depends on internal/schema).
package arch

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	agentaxis "github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/resources"
)

// schemaEnumPaths walks a parsed JSON Schema document and returns every
// "enum" array of string values found, keyed by a "/"-joined path from the
// document root (array indices rendered as decimal segments, e.g.
// "$defs/llmConfig/anyOf/2/properties/role"). This is a discovery sweep, not
// a list of known enum sites — the same property that makes
// vocabulary_adoption_test.go's scan trustworthy: a site added tomorrow is
// found the day it is declared, because nothing here names it in advance.
func schemaEnumPaths(t *testing.T, raw []byte) map[string][]string {
	t.Helper()

	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse config schema: %v", err)
	}

	found := map[string][]string{}
	var walk func(node any, path []string)
	walk = func(node any, path []string) {
		switch v := node.(type) {
		case map[string]any:
			if rawEnum, ok := v["enum"]; ok {
				if list, ok := rawEnum.([]any); ok {
					vals := make([]string, 0, len(list))
					allStrings := true
					for _, item := range list {
						s, ok := item.(string)
						if !ok {
							allStrings = false
							break
						}
						vals = append(vals, s)
					}
					if allStrings && len(vals) > 0 {
						found[strings.Join(path, "/")] = vals
					}
				}
			}
			for k, child := range v {
				walk(child, append(append([]string{}, path...), k))
			}
		case []any:
			for i, child := range v {
				walk(child, append(append([]string{}, path...), fmt.Sprintf("%d", i)))
			}
		}
	}
	walk(doc, nil)
	return found
}

// schemaEnumBinding is one table row: a discovered schema enum path, either
// bound to the Go `*Names()` accessor that must agree with it (goNames
// non-nil, excludeReason empty) or excluded with a stated reason (goNames
// nil, excludeReason non-empty). Never both, never neither —
// TestArch_ConfigSchemaEnums_TableIsWellFormed enforces that shape.
type schemaEnumBinding struct {
	path          string
	goNames       func() []string
	excludeReason string
}

// schemaEnumBindings covers every enum path schemaEnumPaths finds in
// config-schema.json today. See the package doc comment above for the three
// excluded classes and why each is excluded rather than bound.
var schemaEnumBindings = []schemaEnumBinding{
	// Project-default axes (top-level).
	{path: "properties/workspace", goNames: isolation.WorkspaceNames},
	{path: "properties/dirty_tree_handler", goNames: operations.DirtyTreeHandlerNames},
	{path: "properties/runtime", goNames: agentaxis.RuntimeNames},
	{path: "properties/permissions", goNames: agentaxis.PermissionModeNames},

	// Per-agent binding overrides of the same axes.
	{path: "properties/agents/additionalProperties/properties/runtime", goNames: agentaxis.RuntimeNames},
	{path: "properties/agents/additionalProperties/properties/permissions", goNames: agentaxis.PermissionModeNames},
	{path: "properties/agents/additionalProperties/properties/driving", goNames: agents.DrivingModeNames},
	{path: "properties/agents/additionalProperties/properties/config_home", goNames: agents.ConfigHomeNames},

	// Escalation ladder: real Go vocabulary exists but is unexported inside
	// internal/agentcoord/coord (approvalKindNames, LadderAction), a package
	// this test cannot import without an export change to production code —
	// out of scope for a test-only gate — or, for internal/schema, an import
	// cycle (coord -> internal/config -> internal/schema).
	{
		path:          "properties/agents/additionalProperties/properties/escalation/items/properties/kinds/items",
		excludeReason: "real vocabulary is internal/agentcoord/coord.approvalKindNames, unexported; no reachable Names() accessor without a production export change",
	},
	{
		path:          "properties/agents/additionalProperties/properties/escalation/items/properties/action",
		excludeReason: "real vocabulary is internal/agentcoord/coord.LadderAction's consts, unexported; no reachable Names() accessor without a production export change",
	},

	// $defs/hook: claude-code's own hook-handler type vocabulary, passed
	// through verbatim (ClaudeCodeHookWriter.addHook defaults it to
	// "command" and otherwise copies wire.Hook.Type as-is); no ctxloom Go
	// type owns it.
	{
		path:          "$defs/hook/properties/type",
		excludeReason: "claude-code's own hook-handler type vocabulary, passed through verbatim; no ctxloom Go type owns it",
	},
}

func init() {
	// $defs/llmConfig/anyOf has six backend branches. `permissions` and
	// `thinking` (where present) mirror the same ctxloom-owned vocabularies
	// as everywhere else; `role` is registry-only display metadata the
	// schema's own description says is "stripped from persisted user configs
	// and ignored otherwise" — no Go vocabulary backs it, by design, so it is
	// excluded rather than bound. `effort` and `agent_engine` (kiro only)
	// are passthrough to kiro's own CLI flags (kiro.Config.Effort,
	// kiro.Config.AgentEngine are unvalidated strings) — the vendor CLI owns
	// that vocabulary, not ctxloom.
	//
	// Built in init() rather than spelled out six times in the literal above:
	// the six branches are homogeneous in which fields they share, and a
	// loop keeps that homogeneity from silently drifting between branches as
	// a hand-copied literal could.
	branchesWithThinking := map[int]bool{0: true, 1: true, 2: true, 3: true}
	for i := 0; i < 6; i++ {
		prefix := fmt.Sprintf("$defs/llmConfig/anyOf/%d/properties", i)
		schemaEnumBindings = append(schemaEnumBindings,
			schemaEnumBinding{
				path:          prefix + "/role",
				excludeReason: "registry-only display metadata the schema itself says is stripped from persisted user configs and ignored otherwise; no Go vocabulary backs it",
			},
			schemaEnumBinding{path: prefix + "/permissions", goNames: agentaxis.PermissionModeNames},
		)
		if branchesWithThinking[i] {
			schemaEnumBindings = append(schemaEnumBindings,
				schemaEnumBinding{path: prefix + "/thinking", goNames: agentaxis.ThinkingLevelNames})
		}
	}
	// Kiro branch only (anyOf/2): effort and agent_engine.
	schemaEnumBindings = append(schemaEnumBindings,
		schemaEnumBinding{
			path:          "$defs/llmConfig/anyOf/2/properties/effort",
			excludeReason: "passthrough to kiro's own --effort flag (kiro.Config.Effort is an unvalidated string); the vendor CLI owns this vocabulary, not ctxloom",
		},
		schemaEnumBinding{
			path:          "$defs/llmConfig/anyOf/2/properties/agent_engine",
			excludeReason: "passthrough to kiro's own --agent-engine flag (kiro.Config.AgentEngine is an unvalidated string); the vendor CLI owns this vocabulary, not ctxloom",
		},
	)
}

// TestArch_ConfigSchemaEnums_TableIsWellFormed is a fixture-sanity check on
// schemaEnumBindings itself: every row is bound XOR excluded, paths are
// unique, and there is at least one of each kind — a table that was all
// exclusions, or all bindings, would make one of the two gates below
// vacuously trivial.
func TestArch_ConfigSchemaEnums_TableIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	bound, excluded := 0, 0
	for _, b := range schemaEnumBindings {
		if seen[b.path] {
			t.Errorf("duplicate table row for path %q", b.path)
		}
		seen[b.path] = true

		hasNames := b.goNames != nil
		hasReason := b.excludeReason != ""
		if hasNames == hasReason {
			t.Errorf("path %q must set exactly one of goNames or excludeReason, got goNames=%v excludeReason=%q", b.path, hasNames, b.excludeReason)
		}
		if hasNames {
			bound++
		} else {
			excluded++
		}
	}
	if bound == 0 {
		t.Fatal("fixture sanity: no bound row in schemaEnumBindings — TestArch_ConfigSchemaEnums_MatchGoVocabulary would run zero comparisons")
	}
	if excluded == 0 {
		t.Fatal("fixture sanity: no excluded row in schemaEnumBindings — TestArch_ConfigSchemaEnums_ExclusionsAreExplained would run zero checks")
	}
}

// TestArch_ConfigSchemaEnums_TableIsComplete is the SELF-CHECKING piece: the
// set of enum paths schemaEnumPaths discovers in the live schema must equal
// schemaEnumBindings' path set exactly. This is what stops the table from
// being able to silently omit a member the way a hand-maintained coverage
// list could — a newly declared schema enum with no table row fails HERE,
// by path, before either gate below gets a chance to skip it quietly.
func TestArch_ConfigSchemaEnums_TableIsComplete(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	if err != nil {
		t.Fatalf("read config schema: %v", err)
	}
	discovered := schemaEnumPaths(t, raw)
	if len(discovered) == 0 {
		t.Fatal("fixture sanity: schemaEnumPaths found zero enum sites in the live schema — the walker or the schema itself is broken")
	}

	tablePaths := map[string]bool{}
	for _, b := range schemaEnumBindings {
		tablePaths[b.path] = true
	}

	var missing, stale []string
	for p := range discovered {
		if !tablePaths[p] {
			missing = append(missing, p)
		}
	}
	for p := range tablePaths {
		if _, ok := discovered[p]; !ok {
			stale = append(stale, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Errorf("schema declares %d enum path(s) with no schemaEnumBindings row (add a bound or excluded entry for each): %s",
			len(missing), strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Errorf("schemaEnumBindings has %d row(s) naming a path the schema no longer declares an enum for (remove them): %s",
			len(stale), strings.Join(stale, ", "))
	}
}

// TestArch_ConfigSchemaEnums_MatchGoVocabulary is the value-level check for
// every BOUND row: the schema's enum array at that path, sorted, must equal
// the Go accessor's return value, sorted. A schema enum with no bound
// counterpart never reaches this loop (it is either excluded, above, or
// caught as "missing" by the completeness test).
func TestArch_ConfigSchemaEnums_MatchGoVocabulary(t *testing.T) {
	raw, err := resources.GetConfigSchema()
	if err != nil {
		t.Fatalf("read config schema: %v", err)
	}
	discovered := schemaEnumPaths(t, raw)

	for _, b := range schemaEnumBindings {
		if b.goNames == nil {
			continue
		}
		b := b
		t.Run(b.path, func(t *testing.T) {
			schemaVals, ok := discovered[b.path]
			if !ok {
				t.Fatalf("path %q not found in the live schema (TestArch_ConfigSchemaEnums_TableIsComplete should have caught this first)", b.path)
			}
			goVals := b.goNames()
			if len(goVals) == 0 {
				t.Fatalf("fixture sanity: goNames() for %q returned zero values", b.path)
			}

			schemaSorted := append([]string(nil), schemaVals...)
			goSorted := append([]string(nil), goVals...)
			sort.Strings(schemaSorted)
			sort.Strings(goSorted)

			if !equalStrings(schemaSorted, goSorted) {
				t.Errorf("schema enum at %q = %v, Go vocabulary = %v — these must match exactly", b.path, schemaSorted, goSorted)
			}
		})
	}
}

// TestArch_ConfigSchemaEnums_ExclusionsAreExplained asserts every excluded
// row carries a non-trivial reason — the loud-omission half of the
// coordinator's ask. An excluded path with an empty or placeholder reason
// would be a silent gap wearing the shape of a documented one.
func TestArch_ConfigSchemaEnums_ExclusionsAreExplained(t *testing.T) {
	const minReasonLen = 20 // long enough to rule out a placeholder like "todo" or "n/a"
	for _, b := range schemaEnumBindings {
		if b.goNames != nil {
			continue
		}
		if len(strings.TrimSpace(b.excludeReason)) < minReasonLen {
			t.Errorf("path %q is excluded with too short a reason (%q) — state which package/CLI owns the vocabulary instead", b.path, b.excludeReason)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

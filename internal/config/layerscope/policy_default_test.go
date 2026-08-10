package layerscope

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/resources"
)

// TestDefaultPolicy_ExhaustiveAgainstSchema is the exhaustiveness gate the
// design doc promises: "DefaultPolicy is EXHAUSTIVE against the config schema
// by test — a key the schema knows and the policy does not is a failure, so
// no key can be added without its scope being decided." It walks
// resources/schema/input/config-schema.json directly (the raw JSON document,
// not the compiled validator: schema.ConfigValidator's KnownKeys collapses a
// dynamic map's own "additionalProperties" schema into an empty property set,
// which would make a wildcard level like `agents` indistinguishable from a
// genuine leaf) and asserts every LEAF path it finds resolves via
// Policy.Lookup. A leaf is anywhere maps.Flatten would stop: a scalar/array
// value, or an object with neither declared properties nor a dynamic
// additionalProperties schema to recurse into.
func TestDefaultPolicy_ExhaustiveAgainstSchema(t *testing.T) {
	schemaData, err := resources.GetConfigSchema()
	if err != nil {
		t.Fatalf("load config schema: %v", err)
	}
	root := mustParseSchema(t, schemaData)
	policy := DefaultPolicy()

	var missing []string
	walkSchemaLeaves(root, root, nil, func(path []string) {
		joined := strings.Join(path, ".")
		if _, ok := policy.Lookup(path); !ok {
			missing = append(missing, joined)
		}
	})

	if len(missing) > 0 {
		t.Errorf("DefaultPolicy has no rule covering these schema leaves (add a Rule and decide their Scope):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// mustParseSchema decodes the config schema's raw JSON into a generic tree.
func mustParseSchema(t *testing.T, data []byte) map[string]any {
	t.Helper()
	m, err := decodeJSONObject(data)
	if err != nil {
		t.Fatalf("parse config schema JSON: %v", err)
	}
	return m
}

// TestDefaultPolicy_DivergencesFromDesignDoc pins the three keys whose Scope
// here deliberately diverges from the design doc's literal table (each has a
// long inline comment in policy_default.go explaining why). This is a
// regression guard against silently sliding one of them back to the design
// doc's literal (and, per the measured acceptance failures documented
// inline, broken-in-practice) classification.
func TestDefaultPolicy_DivergencesFromDesignDoc(t *testing.T) {
	policy := DefaultPolicy()

	cases := []struct {
		path []string
		want Scope
	}{
		// Design doc: ScopeMachine. Diverges to ScopeShared: an
		// agentBindingMergeFunc-atomic-replaced agent binding can never
		// independently inherit a Machine-scoped runtime once the project
		// names the same agent (as it must, since agents.*.llm is
		// ScopeShared and can only live at project/flag).
		{[]string{"agents", "reviewer", "runtime"}, ScopeShared},
		// Design doc: ScopeMachine. Diverges to ScopeShared: mcp.servers.*
		// entries REQUIRE "command" by schema, so Machine-scoping it would
		// make mcp.servers.* (itself ScopeShared, "what the team wires in")
		// impossible to populate with a working entry from the project
		// layer at all.
		{[]string{"mcp", "servers", "my-server", "command"}, ScopeShared},
		// Design doc: ScopeMachine. Diverges to ScopeShared: `mcp server
		// create --args` (internal/cli/mcp.go's mcpAddArgs) writes straight
		// into the committed project file, the only target it has;
		// Machine-scoping args made that command silently drop exactly the
		// bytes the user typed on its own command line.
		{[]string{"mcp", "servers", "my-server", "args"}, ScopeShared},
		// Design doc: ScopeMachine. Diverges to ScopeShared: `manage
		// statusline` (config.SetStatusline) has no file to persist this
		// preference to but the committed project file; Machine-scoping it
		// makes the toggle silently revert on the next reload.
		{[]string{"config", "statusline"}, ScopeShared},
	}

	for _, tc := range cases {
		rule, ok := policy.Lookup(tc.path)
		if !ok {
			t.Errorf("Lookup(%v): no rule found", tc.path)
			continue
		}
		if rule.Scope != tc.want {
			t.Errorf("Lookup(%v).Scope = %s, want %s (this is a deliberate divergence from the design doc — see policy_default.go)", tc.path, rule.Scope, tc.want)
		}
	}

	// env, mcp.servers.*.command/args' remaining sibling, was NOT
	// reclassified: no CLI verb writes it (mcp server create has no --env
	// flag; mcp server edit refuses a config-level ref), so there is no
	// forcing "the CLI silently drops what I typed" case, and dropping a
	// hand-edited env block is the credential-leak protection this scope
	// exists to provide. Pin that the divergence didn't overreach onto it.
	rule, ok := policy.Lookup([]string{"mcp", "servers", "my-server", "env"})
	if !ok {
		t.Error("Lookup(mcp.servers.*.env): no rule found")
	} else if rule.Scope != ScopeMachine {
		t.Errorf("Lookup(mcp.servers.*.env).Scope = %s, want %s (only .command/.args diverged to Shared)", rule.Scope, ScopeMachine)
	}
}

package layerscope

import (
	"strings"
	"testing"
)

func TestPolicy_Lookup_LongestMatchWins(t *testing.T) {
	p := Policy{
		{Path: "mcp.servers.*", Scope: ScopeShared},
		{Path: "mcp.servers.*.command", Scope: ScopeMachine},
	}

	t.Run("leaf rule beats group rule", func(t *testing.T) {
		rule, ok := p.Lookup([]string{"mcp", "servers", "foo", "command"})
		if !ok {
			t.Fatal("expected a match")
		}
		if rule.Scope != ScopeMachine {
			t.Errorf("got scope %s, want %s (the longer, more specific rule should win)", rule.Scope, ScopeMachine)
		}
	})

	t.Run("group rule covers a sibling leaf the specific rule does not name", func(t *testing.T) {
		rule, ok := p.Lookup([]string{"mcp", "servers", "foo", "notes"})
		if !ok {
			t.Fatal("expected the group rule to cover this")
		}
		if rule.Scope != ScopeShared {
			t.Errorf("got scope %s, want %s", rule.Scope, ScopeShared)
		}
	})

	t.Run("group rule covers the wildcard's own value directly", func(t *testing.T) {
		rule, ok := p.Lookup([]string{"mcp", "servers", "foo"})
		if !ok {
			t.Fatal("expected a match")
		}
		if rule.Scope != ScopeShared {
			t.Errorf("got scope %s, want %s", rule.Scope, ScopeShared)
		}
	})

	t.Run("no match outside the table", func(t *testing.T) {
		if _, ok := p.Lookup([]string{"unrelated", "key"}); ok {
			t.Error("expected no match")
		}
	})
}

func TestPolicy_Lookup_WildcardMatchesExactlyOneSegment(t *testing.T) {
	p := Policy{{Path: "agents.*.runtime", Scope: ScopeMachine}}

	if _, ok := p.Lookup([]string{"agents", "foo", "bar", "runtime"}); ok {
		t.Error("a wildcard segment must not swallow more than one path segment")
	}
	if rule, ok := p.Lookup([]string{"agents", "foo", "runtime"}); !ok || rule.Scope != ScopeMachine {
		t.Error("expected the wildcard to match exactly one segment (\"foo\")")
	}
}

func TestPolicy_Lookup_CaseInsensitive(t *testing.T) {
	p := Policy{{Path: "runtime", Scope: ScopeMachine}}
	if _, ok := p.Lookup([]string{"RUNTIME"}); !ok {
		t.Error("expected a case-insensitive literal match (env resolution can hand back upper-case segments)")
	}
}

func TestPolicy_Check_DropsDisallowedAndKeepsAllowed(t *testing.T) {
	p := Policy{
		{Path: "agents.*.coordinator", Scope: ScopeShared},
		{Path: "agents.*.runtime", Scope: ScopeMachine},
	}
	values := map[string]any{
		"agents": map[string]any{
			"evil": map[string]any{
				"coordinator": true,
				"runtime":     "container",
			},
		},
	}
	violations := p.Check(LayerEnv, values)
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation (coordinator, Shared, disallowed from env), got %d: %+v", len(violations), violations)
	}
	v := violations[0]
	if strings.Join(v.Path, ".") != "agents.evil.coordinator" {
		t.Errorf("violation path = %v, want agents.evil.coordinator", v.Path)
	}
	if v.Layer != LayerEnv {
		t.Errorf("violation layer = %s, want env", v.Layer)
	}
}

func TestPolicy_Check_ArraysAreOneLeafNeverRecursedInto(t *testing.T) {
	// isolation_engines is a []string; a violation on the WHOLE list must be
	// reported once, at the list's own path — never per element (Flatten
	// never descends into a slice).
	p := Policy{{Path: "isolation_engines", Scope: ScopeMachine}}
	values := map[string]any{"isolation_engines": []any{"claude-code", "codex"}}
	violations := p.Check(LayerProject, values)
	if len(violations) != 1 {
		t.Fatalf("expected exactly 1 violation for the whole list, got %d", len(violations))
	}
	if len(violations[0].Path) != 1 || violations[0].Path[0] != "isolation_engines" {
		t.Errorf("expected the violation path to be the list's own single segment, got %v", violations[0].Path)
	}
}

func TestPolicy_Check_EmptyValuesProducesNoViolations(t *testing.T) {
	p := DefaultPolicy()
	if got := p.Check(LayerEnv, nil); got != nil {
		t.Errorf("Check(nil) = %v, want nil", got)
	}
	if got := p.Check(LayerEnv, map[string]any{}); got != nil {
		t.Errorf("Check(empty map) = %v, want nil", got)
	}
}

func TestPolicy_Check_UnknownKeyProducesNoViolation(t *testing.T) {
	// A path the policy has no opinion about is not this package's business
	// (unknown-key handling is separate machinery) -- Check must not invent a
	// violation for it.
	p := Policy{{Path: "runtime", Scope: ScopeMachine}}
	values := map[string]any{"totally_unrelated_key": "x"}
	if got := p.Check(LayerProject, values); got != nil {
		t.Errorf("Check found a violation for an unpoliced key: %v", got)
	}
}

func TestViolation_Message_NamesKeyLayerAndFixIt(t *testing.T) {
	v := Violation{
		Path:  []string{"agents", "reviewer", "coordinator"},
		Layer: LayerHome,
		Rule:  Rule{Path: "agents.*.coordinator", Scope: ScopeShared, Note: "a privilege grant"},
	}
	msg := v.Message("/proj/.ctxloom", "/home/u/.ctxloom")
	for _, want := range []string{
		"home", "agents.reviewer.coordinator", "PROJECT", "a privilege grant",
		"Dropped", "/home/u/.ctxloom/config.yaml", "/proj/.ctxloom/config.yaml",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Message() = %q, missing %q", msg, want)
		}
	}
}

func TestViolation_FixIt_NamesNonFileLayerWhenNoFileAllowed(t *testing.T) {
	// ScopeInvocation allows only env/flag -- neither is a file, so FixIt must
	// name a LAYER, not fabricate a file path.
	v := Violation{
		Path:  []string{"some", "invocation", "key"},
		Layer: LayerProject,
		Rule:  Rule{Path: "some.invocation.key", Scope: ScopeInvocation},
	}
	fixit := v.FixIt("/proj/.ctxloom", "/home/u/.ctxloom")
	if !strings.Contains(fixit, "env") {
		t.Errorf("FixIt() = %q, expected it to name the env layer as the allowed channel", fixit)
	}
}

func TestViolation_FixIt_NeverScopeNamesNoLayer(t *testing.T) {
	v := Violation{
		Path:  []string{"dirty_tree_commit_ack"},
		Layer: LayerEnv,
		Rule:  Rule{Path: "dirty_tree_commit_ack", Scope: ScopeNever},
	}
	fixit := v.FixIt("/proj/.ctxloom", "/home/u/.ctxloom")
	if strings.Contains(fixit, ".ctxloom/config.yaml") {
		t.Errorf("FixIt() = %q, must not suggest a config file for a Never-scope key", fixit)
	}
}

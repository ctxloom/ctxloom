package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
)

// Fixture's own doc promises "a Fixture-built Config never aliases a
// Load result. Every call yields a separately-owned value", and ToFixture /
// NewFixture broke that promise for every map and slice they carried — the
// struct was copied, the containers behind it were not. That re-opened exactly
// the shared-mutation hole the unexported fields plus accessors.go's
// copy-on-read policy exist to close: a Fixture handed out of a Config was a
// live window onto the shared instance every Load() holder sees.
//
// These are CLASS gates, not instance checks. They walk both values
// reflectively, so a field added to Fixture (or to Config) tomorrow and
// wired up without a clone helper fails HERE, at the moment it is added,
// rather than as an action-at-a-distance corruption in the field.

// containerAliases walks v and records the identity of every non-empty map and
// slice it can reach, keyed by backing pointer. Structs and pointers are
// traversed so nested containers (a Profile's []string fields, an MCPServer's
// Env, ...) are covered too — one-level cloning is not enough to keep the
// promise, and this is what says so.
//
// Funcs, chans and unsafe pointers are deliberately skipped: they are not
// containers of user data and Config's (execGate, companionProbe) are not
// carried on a Fixture at all.
func containerAliases(v reflect.Value, path string, out map[uintptr]string) {
	switch v.Kind() {
	case reflect.Map:
		if v.IsNil() || v.Len() == 0 {
			return
		}
		if _, seen := out[v.Pointer()]; !seen {
			out[v.Pointer()] = path
		}
		iter := v.MapRange()
		for iter.Next() {
			containerAliases(iter.Value(), path+"[k]", out)
		}
	case reflect.Slice:
		if v.IsNil() || v.Len() == 0 {
			return
		}
		if _, seen := out[v.Pointer()]; !seen {
			out[v.Pointer()] = path
		}
		for i := range v.Len() {
			containerAliases(v.Index(i), path+"[i]", out)
		}
	case reflect.Pointer:
		if v.IsNil() {
			return
		}
		containerAliases(v.Elem(), "*"+path, out)
	case reflect.Interface:
		if v.IsNil() {
			return
		}
		containerAliases(v.Elem(), path, out)
	case reflect.Struct:
		for i := range v.NumField() {
			containerAliases(v.Field(i), path+"."+v.Type().Field(i).Name, out)
		}
	}
}

// assertNoSharedContainers fails naming every container the two values have in
// common, so the diagnostic points at the field that is missing a clone.
func assertNoSharedContainers(t *testing.T, a, b reflect.Value, aName, bName string) {
	t.Helper()

	left := map[uintptr]string{}
	containerAliases(a, aName, left)
	right := map[uintptr]string{}
	containerAliases(b, bName, right)

	require.NotEmpty(t, left, "the fixture under test must actually populate containers, or this gate proves nothing")
	require.NotEmpty(t, right, "the fixture under test must actually populate containers, or this gate proves nothing")

	for ptr, lp := range left {
		if rp, shared := right[ptr]; shared {
			assert.Fail(t, "Fixture aliases a Config container",
				"%s and %s share one backing container — mutating either corrupts the other, "+
					"contradicting Fixture's documented \"every call yields a separately-owned value\". "+
					"Clone it with the accessors.go helper for its type.", lp, rp)
		}
	}
}

// aliasProbeFixture populates every map and slice Fixture carries — persisted
// AND runtime-only — because an unpopulated container cannot alias and would
// let this gate pass vacuously.
func aliasProbeFixture() Fixture {
	f := fullyPopulatedFixture()
	f.LM.Configs["fast"] = LLMConfig{Type: "claude-code", Body: map[string]any{"model": "m"}}
	f.Editor = EditorConfig{Command: "vi", Args: []string{"-p"}}
	f.Agents["worker"] = agents.Agent{
		LLM:        "fast",
		Profiles:   []string{"p"},
		Escalation: []agents.EscalationRung{{Kinds: []string{"TOOL_USE"}, Action: "auto_accept"}},
	}
	f.AppPaths = []string{"/tmp/app"}
	f.Warnings = []Warning{{Kind: WarnKindUnknownKey, Text: "w"}}
	return f
}

// TestToFixture_NeverAliasesConfigContainers is the read half: a Fixture taken
// off a Config must be independently owned, or the "copy" callers amend is a
// live handle on the shared instance.
func TestToFixture_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	f := cfg.ToFixture()

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(f), "Config", "Fixture")
}

// TestNewFixture_NeverAliasesItsFixture is the write half: the Config built
// from a Fixture must not keep the caller's containers either, or a caller
// that reuses its Fixture (the ToFixture -> amend -> NewFixture round trip
// operations.BuildInitialConfig performs) can still reach inside the result.
func TestNewFixture_NeverAliasesItsFixture(t *testing.T) {
	f := aliasProbeFixture()

	cfg := NewFixture(f)

	assertNoSharedContainers(t, reflect.ValueOf(f), reflect.ValueOf(cfg).Elem(), "Fixture", "Config")
}

// TestToFixture_MutationDoesNotReachConfig states the same contract as
// behaviour: several tests used to write a profile definition through
// `cfg.ToFixture().Profiles.Definitions[...] = ...` and rely on it landing in
// the ambient config. It must not.
func TestToFixture_MutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	cfg.ToFixture().Agents["injected"] = agents.Agent{LLM: "fast"}

	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected",
		"a Fixture is a copy: writing into its Agents must not mutate the Config it came from")
}

// TestNewFixture_MutatingTheSourceFixtureDoesNotReachTheConfig is the same
// contract from the other side.
func TestNewFixture_MutatingTheSourceFixtureDoesNotReachTheConfig(t *testing.T) {
	f := aliasProbeFixture()

	cfg := NewFixture(f)
	f.Agents["injected"] = agents.Agent{LLM: "fast"}

	assert.NotContains(t, cfg.GetConfiguredAgents(), "injected",
		"NewFixture must take ownership of its own containers, not the caller's")
}

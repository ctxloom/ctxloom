//go:build parked_engines

package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hookCfg builds a codex config map with one [hooks.EVENT] array-of-tables
// group holding the given commands.
func hookCfg(event string, commands ...string) map[string]any {
	entries := make([]any, 0, len(commands))
	for _, cmd := range commands {
		entries = append(entries, map[string]any{"command": cmd})
	}
	return map[string]any{
		"hooks": map[string]any{
			event: []any{map[string]any{"hooks": entries}},
		},
	}
}

// deepCopy snapshots a decoded-TOML value so a "did this mutate?" assertion has
// something to compare against.
func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopy(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, val := range t {
			out = append(out, deepCopy(val))
		}
		return out
	default:
		return v
	}
}

// TestManagedHooks_QueryAgreesWithRemoval is the parity gate between the two
// managed-hook operations.
//
// Giving hasManagedHook and removeManagedHooks each their OWN copy of codex's
// three-level descent (event -> groups -> hooks[] -> entry["command"] ->
// agent.IsManaged) means two hand-maintained walks over one shape, kept in sync
// by nobody — and the pruning one sits at the project's CCN-10 ceiling, so any
// change to the shape has to be made twice AND squeezed under the gate.
//
// The two are definitionally linked: hasManagedHook is true exactly when
// removeManagedHooks has work to do. This states that link as a test across a
// table of config shapes, INCLUDING the degenerate ones where two independent
// traversals are most likely to disagree (a non-map group, an empty entry list,
// a group with no hooks key at all, several events at once).
//
// It also pins the property nothing stated before: the QUERY must not mutate.
func TestManagedHooks_QueryAgreesWithRemoval(t *testing.T) {
	const managed = "ctxloom hook inject-context"
	const foreign = "/usr/bin/make lint"

	cases := []struct {
		name string
		cfg  func() map[string]any
		want bool
	}{
		{"no hooks table at all", func() map[string]any { return map[string]any{} }, false},
		{"only foreign hooks", func() map[string]any { return hookCfg("PreToolUse", foreign) }, false},
		{"one managed hook", func() map[string]any { return hookCfg("SessionStart", managed) }, true},
		{"managed alongside foreign", func() map[string]any { return hookCfg("PreToolUse", foreign, managed) }, true},
		{"managed in a later event", func() map[string]any {
			cfg := hookCfg("PreToolUse", foreign)
			cfg["hooks"].(map[string]any)["SessionStart"] = []any{map[string]any{"hooks": []any{map[string]any{"command": managed}}}}
			return cfg
		}, true},
		{"empty entry list", func() map[string]any { return hookCfg("PreToolUse") }, false},
		{"group is not a table", func() map[string]any {
			return map[string]any{"hooks": map[string]any{"PreToolUse": []any{"not-a-table"}}}
		}, false},
		{"group has no hooks key", func() map[string]any {
			return map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash"}}}}
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The query answers what the row claims it answers.
			query := tc.cfg()
			before := deepCopy(query)
			got := hasManagedHook(query)
			assert.Equal(t, tc.want, got, "hasManagedHook")
			assert.Equal(t, before, deepCopy(query),
				"the boolean query must not mutate the config it is only reading")

			// And it agrees with the removal: true exactly when removal changes something.
			removal := tc.cfg()
			pre := deepCopy(removal)
			removeManagedHooks(removal)
			changed := !assert.ObjectsAreEqual(pre, deepCopy(removal))
			assert.Equal(t, got, changed,
				"hasManagedHook must be true exactly when removeManagedHooks has work to do")

			// Removal is idempotent and leaves nothing managed behind.
			require.False(t, hasManagedHook(removal), "removal must leave no managed hook behind")
		})
	}
}

// TestRemoveManagedHooks_PreservesForeignEntries pins that the pruning walk
// touches only ctxloom's own entries — the "never wipe wholesale" rule this
// package applies to every shared surface.
func TestRemoveManagedHooks_PreservesForeignEntries(t *testing.T) {
	cfg := hookCfg("PreToolUse", "/usr/bin/make lint", "ctxloom hook inject-context", "./scripts/check.sh")
	removeManagedHooks(cfg)

	groups := cfg["hooks"].(map[string]any)["PreToolUse"].([]any)
	require.Len(t, groups, 1)
	entries := groups[0].(map[string]any)["hooks"].([]any)
	require.Len(t, entries, 2, "both foreign hooks must survive")
	assert.Equal(t, "/usr/bin/make lint", entries[0].(map[string]any)["command"])
	assert.Equal(t, "./scripts/check.sh", entries[1].(map[string]any)["command"])
}

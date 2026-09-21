package wire

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

// MergeHooksConfig is the nil-handling wrapper over HooksConfig.Append that
// every hook composition builds on. It lives with the types so the profile
// resolver — a core package — reaches it without the engine base.

func TestMergeHooksConfig_AppendsAndDedupes(t *testing.T) {
	dest := &HooksConfig{Unified: UnifiedHooks{PreTool: []Hook{{Command: "existing"}}}}
	src := &HooksConfig{Unified: UnifiedHooks{
		PreTool:      []Hook{{Command: "existing"}, {Command: "added"}},
		SessionStart: []Hook{{Command: "start"}},
	}}
	dropped := MergeHooksConfig(dest, src)
	assert.Zero(t, dropped)
	assert.Equal(t, []Hook{{Command: "existing"}, {Command: "added"}}, dest.Unified.PreTool, "the same hook never runs twice in one event")
	assert.Equal(t, []Hook{{Command: "start"}}, dest.Unified.SessionStart)
}

func TestMergeHooksConfig_NilSource_IsANoOp(t *testing.T) {
	dest := &HooksConfig{Unified: UnifiedHooks{PreTool: []Hook{{Command: "keep"}}}}
	assert.Zero(t, MergeHooksConfig(dest, nil))
	assert.Equal(t, []Hook{{Command: "keep"}}, dest.Unified.PreTool)
	assert.Zero(t, MergeHooksConfig(nil, nil))
}

func TestMergeHooksConfig_NilDestination_ReportsWhatItDropped(t *testing.T) {
	src := &HooksConfig{
		Unified: UnifiedHooks{PreTool: []Hook{{Command: "a"}}, TurnEnd: []Hook{{Command: "b"}}},
		Ext:     map[string]BackendHooks{"x": {"ev": []Hook{{Command: "c"}}}},
	}
	assert.Equal(t, 3, MergeHooksConfig(nil, src), "a merge with nowhere to go names the SIZE of what it dropped so the caller can say so")
	assert.Zero(t, MergeHooksConfig(nil, &HooksConfig{}), "an empty source dropped is nothing dropped")
}

// TestHooksConfig_Count_CountsEveryUnifiedEvent keeps the drop-report honest.
// Count is a hand-written sum over UnifiedHooks' fields and its consumer is
// the "N hooks lost" warning — an event missing from the sum makes a whole
// hook set vanish while the warning under-reports the loss, or reports none
// at all when the lost set was entirely that event.
func TestHooksConfig_Count_CountsEveryUnifiedEvent(t *testing.T) {
	typ := reflect.TypeOf(UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			var u UnifiedHooks
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]Hook{{Command: "x"}}))
			assert.Equal(t, 1, (&HooksConfig{Unified: u}).Count(),
				"a lone %s hook must be counted, or the loss it represents is reported as nothing", name)
		})
	}
}

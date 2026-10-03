package wire

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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

// TestHooksConfig_Append_EmptySourceLeavesExtNil pins the nil/declared-empty
// distinction: a nil Ext says "no engine-namespace hooks were declared", an
// empty one says "declared, and empty". Merging a source with nothing in Ext
// must not turn the first into the second. The serialized form is asserted
// alongside so the fix is proven not to move any settings file a byte —
// omitempty already hides an empty map, which is exactly why the in-memory
// difference went unnoticed.
func TestHooksConfig_Append_EmptySourceLeavesExtNil(t *testing.T) {
	dest := HooksConfig{Unified: UnifiedHooks{PreTool: []Hook{{Command: "keep"}}}}
	pristine := dest

	dest.Append(HooksConfig{Unified: UnifiedHooks{PreTool: []Hook{{Command: "added"}}}})

	assert.Nil(t, dest.Ext, "merging a source with no Ext must leave a nil Ext nil")

	pristine.Unified.PreTool = dest.Unified.PreTool
	wantJSON, err := json.Marshal(pristine)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(dest)
	require.NoError(t, err)
	assert.Equal(t, string(wantJSON), string(gotJSON))

	wantYAML, err := yaml.Marshal(pristine)
	require.NoError(t, err)
	gotYAML, err := yaml.Marshal(dest)
	require.NoError(t, err)
	assert.Equal(t, string(wantYAML), string(gotYAML))
}

// TestHooksConfig_Append_ExtSourceStillAllocates is the other half: a source
// that does carry engine hooks lands them in a nil destination.
func TestHooksConfig_Append_ExtSourceStillAllocates(t *testing.T) {
	var dest HooksConfig
	dest.Append(HooksConfig{Ext: map[string]BackendHooks{"claude-code": {"PreToolUse": []Hook{{Command: "x"}}}}})
	require.NotNil(t, dest.Ext)
	assert.Equal(t, []Hook{{Command: "x"}}, dest.Ext["claude-code"]["PreToolUse"])
}

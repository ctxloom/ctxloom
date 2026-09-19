package wire

import (
	"reflect"
	"testing"
)

func TestHooksConfig_HasAny(t *testing.T) {
	tests := []struct {
		name string
		cfg  HooksConfig
		want bool
	}{
		{"empty", HooksConfig{}, false},
		{
			"unified event populated",
			HooksConfig{Unified: UnifiedHooks{PostTool: []Hook{{Command: "x"}}}},
			true,
		},
		{
			"plugin event populated",
			HooksConfig{Plugins: map[string]BackendHooks{
				"claude-code": {"PostToolUse": []Hook{{Command: "x"}}},
			}},
			true,
		},
		{
			"plugin present but empty",
			HooksConfig{Plugins: map[string]BackendHooks{"claude-code": {}}},
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.HasAny(); got != tt.want {
				t.Errorf("HasAny() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnifiedHooks_Append(t *testing.T) {
	dst := UnifiedHooks{PostTool: []Hook{{Matcher: "X", Command: "x"}}}
	src := UnifiedHooks{
		PostTool:     []Hook{{Matcher: "Y", Command: "y"}},
		PostFileEdit: []Hook{{Matcher: "Z", Command: "z"}},
	}

	dst.Append(src)

	if len(dst.PostTool) != 2 {
		t.Fatalf("PostTool len = %d, want 2", len(dst.PostTool))
	}
	if dst.PostTool[1].Matcher != "Y" {
		t.Errorf("PostTool[1].Matcher = %q, want %q", dst.PostTool[1].Matcher, "Y")
	}
	if len(dst.PostFileEdit) != 1 {
		t.Errorf("PostFileEdit len = %d, want 1", len(dst.PostFileEdit))
	}
}

// TestUnifiedHooks_Append_AllEvents is REFLECTION-DRIVEN over UnifiedHooks'
// own fields rather than a hand-written list of them. Append's doc comment
// names the exact drift this guards: a new unified event added to the struct
// and not added to Append is dropped SILENTLY — it compiles, it merges, and the
// hook simply never arrives. A hand-enumerated assertion cannot see that,
// because the enumeration is the thing that went stale.
func TestUnifiedHooks_Append_AllEvents(t *testing.T) {
	typ := reflect.TypeOf(UnifiedHooks{})

	var src UnifiedHooks
	srcVal := reflect.ValueOf(&src).Elem()
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		srcVal.Field(i).Set(reflect.ValueOf([]Hook{{Command: "cmd-" + name}}))
	}

	var dst UnifiedHooks
	dst.Append(src)

	dstVal := reflect.ValueOf(dst)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		got, _ := dstVal.Field(i).Interface().([]Hook)
		if len(got) != 1 {
			t.Errorf("%s: len = %d, want 1 — Append does not carry this event", name, len(got))
			continue
		}
		if got[0].Command != "cmd-"+name {
			t.Errorf("%s: carried %q, want %q — Append crossed two events' slices", name, got[0].Command, "cmd-"+name)
		}
	}
}

// Every unified event on its own must make HasAny true. Same reflection
// argument as above: HasAny is a hand-written sum, and an event missing from it
// makes config Save() delete the whole `hooks` key from the user's file — a
// silent loss of everything they wrote there, not just the new event.
func TestHooksConfig_HasAny_EveryUnifiedEventCounts(t *testing.T) {
	typ := reflect.TypeOf(UnifiedHooks{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		t.Run(name, func(t *testing.T) {
			var u UnifiedHooks
			reflect.ValueOf(&u).Elem().Field(i).Set(reflect.ValueOf([]Hook{{Command: "x"}}))
			if !(HooksConfig{Unified: u}).HasAny() {
				t.Errorf("a lone %s hook must make HasAny() true, or config Save() drops the entire hooks key", name)
			}
		})
	}
}

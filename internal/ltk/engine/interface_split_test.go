package engine

import (
	"reflect"
	"testing"
)

// U067-F05 says Engine is "two interfaces under one name" and asks for the
// split. The split is already here and named: Adapter is the RUNTIME surface
// (Name/Decode/Encode, what cmd/ltk evaluate needs) and Engine is Adapter plus
// the five management methods (what cmd/ltk manage needs). This pins that
// composition so it cannot quietly regress into the single fat interface the
// row describes — which is what would have to happen for the claim to become
// true.
func TestAdapterAndEngineStayComposed(t *testing.T) {
	adapter := reflect.TypeOf((*Adapter)(nil)).Elem()
	full := reflect.TypeOf((*Engine)(nil)).Elem()

	runtimeMethods := map[string]bool{"Name": true, "Decode": true, "Encode": true}
	managementMethods := map[string]bool{
		"Detect": true, "SettingsPath": true, "HookCommand": true,
		"Install": true, "Uninstall": true,
	}

	if got := methodNames(adapter); !sameSet(got, runtimeMethods) {
		t.Errorf("Adapter's method set is %v; the runtime surface is %v — a management method leaked into it",
			got, keys(runtimeMethods))
	}

	want := map[string]bool{}
	for k := range runtimeMethods {
		want[k] = true
	}
	for k := range managementMethods {
		want[k] = true
	}
	if got := methodNames(full); !sameSet(got, want) {
		t.Errorf("Engine's method set is %v, want %v", got, keys(want))
	}

	// Engine must remain a SUPERSET of Adapter, so anything holding an Engine
	// can be handed to a runtime-only consumer without a conversion.
	if !full.Implements(adapter) {
		t.Error("Engine no longer satisfies Adapter: the two surfaces have been split apart rather than composed")
	}

	// Every registered engine implements both, which is what lets All() feed
	// both the runtime and the management paths.
	for _, e := range All() {
		if _, ok := e.(Adapter); !ok {
			t.Errorf("%T does not implement Adapter", e)
		}
	}
}

func methodNames(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumMethod(); i++ {
		out[t.Method(i).Name] = true
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

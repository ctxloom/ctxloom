package engine

import "testing"

// childMap and childSlice are the two halves of one accessor pair over the same
// untyped settings document, so they must have the SAME contract about who owns
// the write-back. childSlice CANNOT own it — the caller appends to the returned
// slice and append may reallocate, so the caller must write the result back
// regardless. That makes "the accessor never stores; the caller always writes
// back" the only contract both can honour, and childMap storing its freshly
// created map behind the caller's back the odd one out (U067-F10).
func TestChildAccessors_NeverStoreIntoParent(t *testing.T) {
	t.Run("childMap", func(t *testing.T) {
		parent := map[string]any{}
		child, err := childMap(parent, keyHooks)
		if err != nil {
			t.Fatalf("childMap: %v", err)
		}
		if child == nil {
			t.Fatal("childMap returned a nil map for an absent key")
		}
		if _, stored := parent[keyHooks]; stored {
			t.Errorf("childMap wrote %q back into the parent; childSlice does not, and the caller writes back either way", keyHooks)
		}
	})

	t.Run("childSlice", func(t *testing.T) {
		parent := map[string]any{}
		child, err := childSlice(parent, keyPreToolUse)
		if err != nil {
			t.Fatalf("childSlice: %v", err)
		}
		if child == nil {
			t.Fatal("childSlice returned a nil slice for an absent key")
		}
		if _, stored := parent[keyPreToolUse]; stored {
			t.Errorf("childSlice wrote %q back into the parent", keyPreToolUse)
		}
	})
}

// A wrong-typed existing value must error from BOTH accessors rather than being
// silently replaced — the user's settings document is not ours to overwrite.
func TestChildAccessors_RejectWrongType(t *testing.T) {
	if _, err := childMap(map[string]any{keyHooks: "nope"}, keyHooks); err == nil {
		t.Error("childMap accepted a string where an object was required")
	}
	if _, err := childSlice(map[string]any{keyPreToolUse: "nope"}, keyPreToolUse); err == nil {
		t.Error("childSlice accepted a string where an array was required")
	}
}

// An existing value of the right type is returned as-is (identity preserved, so
// the caller's mutations land on the user's own sub-document).
func TestChildAccessors_ReturnExistingValue(t *testing.T) {
	existing := map[string]any{"keep": true}
	parent := map[string]any{keyHooks: existing}
	got, err := childMap(parent, keyHooks)
	if err != nil {
		t.Fatalf("childMap: %v", err)
	}
	if _, ok := got["keep"]; !ok {
		t.Error("childMap did not return the existing sub-document")
	}

	existingSlice := []any{"a"}
	parentSlice := map[string]any{keyPreToolUse: existingSlice}
	gotSlice, err := childSlice(parentSlice, keyPreToolUse)
	if err != nil {
		t.Fatalf("childSlice: %v", err)
	}
	if len(gotSlice) != 1 {
		t.Errorf("childSlice returned %d entries, want the existing 1", len(gotSlice))
	}
}

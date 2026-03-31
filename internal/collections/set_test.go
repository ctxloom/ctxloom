// Package collections tests verify the generic Set type provides correct
// uniqueness guarantees and collection operations. Sets are used throughout
// ctxloom to deduplicate tags, fragment names, and other collections where
// uniqueness matters.
package collections

import (
	"slices"
	"testing"
)

// =============================================================================
// Set Construction Tests
// =============================================================================
// Sets must initialize correctly and deduplicate elements on creation.

func TestNewSet(t *testing.T) {
	s := NewSet[string]()
	if len(s.Items()) != 0 {
		t.Errorf("expected empty set, got len %d", len(s.Items()))
	}
}

func TestNewSetFrom(t *testing.T) {
	// Duplicates in input should be collapsed - essential for tag deduplication
	s := NewSetFrom("a", "b", "c", "a") // duplicate "a"
	if len(s.Items()) != 3 {
		t.Errorf("expected 3 elements, got %d", len(s.Items()))
	}
	if !s.Has("a") || !s.Has("b") || !s.Has("c") {
		t.Error("missing expected elements")
	}
}

// =============================================================================
// Set Mutation Tests
// =============================================================================
// Sets must maintain uniqueness invariant across all mutation operations.

func TestSet_Add(t *testing.T) {
	// Adding duplicates must not increase size - this is the core set property
	s := NewSet[int]()
	s.Add(1)
	s.Add(2)
	s.Add(1) // duplicate

	if len(s.Items()) != 2 {
		t.Errorf("expected 2 elements, got %d", len(s.Items()))
	}
}

func TestSet_AddAll(t *testing.T) {
	// Batch adds must also deduplicate - used when merging tag lists
	s := NewSet[string]()
	s.AddAll("x", "y", "z", "x")

	if len(s.Items()) != 3 {
		t.Errorf("expected 3 elements, got %d", len(s.Items()))
	}
}

func TestSet_Has(t *testing.T) {
	// Membership checks must be accurate - used for filtering duplicates
	s := NewSetFrom("exists")

	if !s.Has("exists") {
		t.Error("expected Has to return true for existing element")
	}
	if s.Has("missing") {
		t.Error("expected Has to return false for missing element")
	}
}

// =============================================================================
// Set Export Tests
// =============================================================================
// Sets must provide reliable conversion to slices for iteration and output.

func TestSet_Items(t *testing.T) {
	// Items provides deterministic iteration when sorted
	s := NewSetFrom("a", "b", "c")
	items := s.Items()

	if len(items) != 3 {
		t.Errorf("expected 3 items, got %d", len(items))
	}

	slices.Sort(items)
	expected := []string{"a", "b", "c"}
	if !slices.Equal(items, expected) {
		t.Errorf("expected %v, got %v", expected, items)
	}
}

func TestSet_Clone(t *testing.T) {
	// Clone must create independent copy - mutations to clone must not affect original
	original := NewSetFrom("a", "b")
	clone := original.Clone()

	if len(clone.Items()) != len(original.Items()) {
		t.Error("clone should have same length")
	}
	if !clone.Has("a") || !clone.Has("b") {
		t.Error("clone missing elements")
	}

	// Verify independence - this is critical for safe concurrent operations
	clone.Add("c")
	if original.Has("c") {
		t.Error("modifying clone should not affect original")
	}
}

// =============================================================================
// Generic Type Support Tests
// =============================================================================
// Sets must work with any comparable type, not just strings.

func TestSet_IntType(t *testing.T) {
	// Integer sets used for numeric ID deduplication
	s := NewSetFrom(1, 2, 3)
	if len(s.Items()) != 3 {
		t.Errorf("expected 3 elements, got %d", len(s.Items()))
	}
	if !s.Has(2) {
		t.Error("expected Has(2) to be true")
	}
}

type customStruct struct {
	ID   int
	Name string
}

func TestSet_StructType(t *testing.T) {
	// Struct sets work via value equality - useful for complex deduplication
	s := NewSet[customStruct]()
	s.Add(customStruct{1, "one"})
	s.Add(customStruct{2, "two"})
	s.Add(customStruct{1, "one"}) // duplicate by value

	if len(s.Items()) != 2 {
		t.Errorf("expected 2 elements, got %d", len(s.Items()))
	}
	if !s.Has(customStruct{1, "one"}) {
		t.Error("expected struct to be in set")
	}
}

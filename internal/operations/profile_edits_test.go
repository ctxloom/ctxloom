package operations

import (
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/profiles"
)

// applyListEdits is the shared add/remove primitive behind UpdateProfile's
// per-field mutations. These tests pin the behaviors UpdateProfile relies on:
// dedup-on-add, remove-first-match, no-op on absent, and add-before-remove
// change ordering.
func TestApplyListEdits(t *testing.T) {
	t.Run("adds new items and records a change each", func(t *testing.T) {
		list, changes := applyListEdits(nil, []string{"a", "b"}, nil, "tag", nil)
		if want := []string{"a", "b"}; !slices.Equal(list, want) {
			t.Fatalf("list = %v, want %v", list, want)
		}
		if want := []string{"added tag: a", "added tag: b"}; !slices.Equal(changes, want) {
			t.Fatalf("changes = %v, want %v", changes, want)
		}
	})

	t.Run("dedups items already present (no change recorded)", func(t *testing.T) {
		list, changes := applyListEdits([]string{"a"}, []string{"a"}, nil, "tag", nil)
		if want := []string{"a"}; !slices.Equal(list, want) {
			t.Fatalf("list = %v, want %v", list, want)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %v, want none", changes)
		}
	})

	t.Run("removes the first matching item and records a change", func(t *testing.T) {
		list, changes := applyListEdits([]string{"a", "b", "c"}, nil, []string{"b"}, "bundle", nil)
		if want := []string{"a", "c"}; !slices.Equal(list, want) {
			t.Fatalf("list = %v, want %v", list, want)
		}
		if want := []string{"removed bundle: b"}; !slices.Equal(changes, want) {
			t.Fatalf("changes = %v, want %v", changes, want)
		}
	})

	t.Run("remove of an absent item is a no-op", func(t *testing.T) {
		list, changes := applyListEdits([]string{"a"}, nil, []string{"x"}, "tag", nil)
		if want := []string{"a"}; !slices.Equal(list, want) {
			t.Fatalf("list = %v, want %v", list, want)
		}
		if len(changes) != 0 {
			t.Fatalf("changes = %v, want none", changes)
		}
	})

	t.Run("nil add and remove leaves list and changes untouched", func(t *testing.T) {
		list, changes := applyListEdits([]string{"a"}, nil, nil, "tag", []string{"prior"})
		if want := []string{"a"}; !slices.Equal(list, want) {
			t.Fatalf("list = %v, want %v", list, want)
		}
		if want := []string{"prior"}; !slices.Equal(changes, want) {
			t.Fatalf("changes = %v, want %v", changes, want)
		}
	})

	t.Run("applies all adds before any removes in change order", func(t *testing.T) {
		_, changes := applyListEdits([]string{"keep", "drop"}, []string{"new"}, []string{"drop"}, "tag", nil)
		want := []string{"added tag: new", "removed tag: drop"}
		if !slices.Equal(changes, want) {
			t.Fatalf("changes = %v, want %v", changes, want)
		}
	})
}

// requireProfilesExist gates parent additions: it must error on the first
// unknown name so UpdateProfile halts before mutating or saving.
func TestRequireProfilesExist(t *testing.T) {
	tmp := t.TempDir()
	loader := profiles.NewLoader([]string{tmp})
	if err := loader.Save(&profiles.Profile{Name: "exists", Bundles: []string{"go-development"}}); err != nil {
		t.Fatalf("save: %v", err)
	}

	t.Run("nil names is fine", func(t *testing.T) {
		if err := requireProfilesExist(loader, nil); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("all existing is fine", func(t *testing.T) {
		if err := requireProfilesExist(loader, []string{"exists"}); err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
	})

	t.Run("first unknown errors", func(t *testing.T) {
		err := requireProfilesExist(loader, []string{"exists", "nope"})
		if err == nil {
			t.Fatal("expected an error for unknown parent")
		}
	})

	t.Run("remote refs skip local validation", func(t *testing.T) {
		// Remote parents only exist locally after `deps pull`, and pull only
		// fetches what a profile references; they are validated at pull/lock time.
		if err := requireProfilesExist(loader, []string{"https://github.com/test/forge@profiles/base"}); err != nil {
			t.Fatalf("err = %v, want nil for a remote ref", err)
		}
	})
}

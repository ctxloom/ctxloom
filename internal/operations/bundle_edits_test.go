package operations

import (
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
)

// applyFragmentEdits merges fragment inputs and reports which names need
// (re)distillation. A new fragment is queued; a content change wipes cached
// distill state and re-queues; a metadata-only edit must NOT re-queue or wipe
// distilled state (that would burn tokens for no semantic change).
func TestApplyFragmentEdits(t *testing.T) {
	t.Run("new fragment is set and queued for distill", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b"}
		changes, distill := applyFragmentEdits(b, map[string]BundleFragmentInput{
			"f1": {Content: "hello"},
		}, nil, nil)

		if got := b.Fragments["f1"].Content; got != "hello" {
			t.Fatalf("content = %q, want hello", got)
		}
		wantContains(t, changes, "set fragment: f1")
		if !slices.Contains(distill, "f1") {
			t.Errorf("distill targets %v should include f1", distill)
		}
	})

	t.Run("NoDistill fragment is set but not queued", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b"}
		_, distill := applyFragmentEdits(b, map[string]BundleFragmentInput{
			"f1": {Content: "hello", NoDistill: true},
		}, nil, nil)
		if len(distill) != 0 {
			t.Errorf("distill targets = %v, want none", distill)
		}
	})

	t.Run("metadata-only edit preserves distilled state and does not re-queue", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Fragments: map[string]bundles.BundleFragment{
			"f1": {Content: "same", Distilled: "D", DistilledBy: "model", ContentHash: "H"},
		}}
		_, distill := applyFragmentEdits(b, map[string]BundleFragmentInput{
			"f1": {Content: "same", Tags: []string{"new-tag"}},
		}, nil, nil)

		if len(distill) != 0 {
			t.Errorf("metadata-only edit should not queue distill, got %v", distill)
		}
		got := b.Fragments["f1"]
		if got.Distilled != "D" || got.DistilledBy != "model" || got.ContentHash != "H" {
			t.Errorf("distilled state should be preserved, got %+v", got)
		}
		if !slices.Equal(got.Tags, []string{"new-tag"}) {
			t.Errorf("tags should update, got %v", got.Tags)
		}
	})

	t.Run("content change wipes distilled state and re-queues", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Fragments: map[string]bundles.BundleFragment{
			"f1": {Content: "old", Distilled: "D", DistilledBy: "model", ContentHash: "H"},
		}}
		_, distill := applyFragmentEdits(b, map[string]BundleFragmentInput{
			"f1": {Content: "new"},
		}, nil, nil)

		if !slices.Contains(distill, "f1") {
			t.Errorf("content change should re-queue f1, got %v", distill)
		}
		got := b.Fragments["f1"]
		if got.Distilled != "" || got.DistilledBy != "" || got.ContentHash != "" {
			t.Errorf("distilled state should be wiped, got %+v", got)
		}
	})

	t.Run("remove present fragment, no-op on absent", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Fragments: map[string]bundles.BundleFragment{
			"doomed": {Content: "x"},
		}}
		changes, _ := applyFragmentEdits(b, nil, []string{"doomed", "ghost"}, nil)
		if _, ok := b.Fragments["doomed"]; ok {
			t.Error("doomed should be removed")
		}
		wantContains(t, changes, "removed fragment: doomed")
		wantAbsent(t, changes, "removed fragment: ghost")
	})
}

// applyPromptEdits mirrors applyFragmentEdits and also carries Description.
func TestApplyPromptEdits(t *testing.T) {
	b := &bundles.Bundle{Name: "b"}
	changes, distill := applyPromptEdits(b, map[string]BundleCommandInput{
		"p1": {Content: "c", Description: "d"},
	}, nil, nil)

	if got := b.Commands["p1"]; got.Content != "c" || got.Description != "d" {
		t.Fatalf("prompt = %+v, want content c / description d", got)
	}
	wantContains(t, changes, "set prompt: p1")
	if !slices.Contains(distill, "p1") {
		t.Errorf("distill targets %v should include p1", distill)
	}

	changes, _ = applyPromptEdits(b, nil, []string{"p1"}, nil)
	if _, ok := b.Commands["p1"]; ok {
		t.Error("p1 should be removed")
	}
	wantContains(t, changes, "removed prompt: p1")
}

// applyMCPEdits sets/removes MCP servers (no distillation involved).
func TestApplyMCPEdits(t *testing.T) {
	b := &bundles.Bundle{Name: "b"}
	changes := applyMCPEdits(b, map[string]BundleMCPInput{
		"srv": {Command: "run-me", Args: []string{"--flag"}},
	}, nil, nil)

	if got := b.MCP["srv"]; got.Command != "run-me" || !slices.Equal(got.Args, []string{"--flag"}) {
		t.Fatalf("mcp = %+v, want command run-me / args [--flag]", got)
	}
	wantContains(t, changes, "set mcp: srv")

	changes = applyMCPEdits(b, nil, []string{"srv", "absent"}, nil)
	if _, ok := b.MCP["srv"]; ok {
		t.Error("srv should be removed")
	}
	wantContains(t, changes, "removed mcp: srv")
	wantAbsent(t, changes, "removed mcp: absent")
}

func wantContains(t *testing.T, changes []string, want string) {
	t.Helper()
	if !slices.Contains(changes, want) {
		t.Errorf("changes %v missing %q", changes, want)
	}
}

func wantAbsent(t *testing.T, changes []string, notWant string) {
	t.Helper()
	if slices.Contains(changes, notWant) {
		t.Errorf("changes %v should not contain %q", changes, notWant)
	}
}

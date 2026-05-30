package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/iox"
)

func newTestBundle() *bundles.Bundle {
	return &bundles.Bundle{Name: "b"}
}

func TestApplyBundleScalarEdits(t *testing.T) {
	t.Run("sets description and version", func(t *testing.T) {
		b := newTestBundle()
		if !applyBundleScalarEdits(b, bundleEdits{Description: "d", Version: "1.2"}) {
			t.Fatal("expected modified=true")
		}
		if b.Description != "d" || b.Version != "1.2" {
			t.Fatalf("got desc=%q ver=%q", b.Description, b.Version)
		}
	})

	t.Run("empty edits are a no-op", func(t *testing.T) {
		b := newTestBundle()
		if applyBundleScalarEdits(b, bundleEdits{}) {
			t.Fatal("expected modified=false for empty edits")
		}
	})
}

func TestApplyBundleTagEdits(t *testing.T) {
	t.Run("adds new tag, skips duplicate", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Tags: []string{"keep"}}
		if !applyBundleTagEdits(b, bundleEdits{AddTags: []string{"new", "keep"}}) {
			t.Fatal("expected modified=true")
		}
		if got := strings.Join(b.Tags, ","); got != "keep,new" {
			t.Fatalf("tags = %q, want keep,new (dedup)", got)
		}
	})

	t.Run("removes present tag", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Tags: []string{"a", "b"}}
		applyBundleTagEdits(b, bundleEdits{RemoveTags: []string{"a"}})
		if got := strings.Join(b.Tags, ","); got != "b" {
			t.Fatalf("tags = %q, want b", got)
		}
	})

	// Documented quirk: removing an absent tag still reports modified=true.
	t.Run("removing an absent tag still reports modified (preserved quirk)", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Tags: []string{"a"}}
		if !applyBundleTagEdits(b, bundleEdits{RemoveTags: []string{"ghost"}}) {
			t.Fatal("expected modified=true even though 'ghost' was absent")
		}
		if got := strings.Join(b.Tags, ","); got != "a" {
			t.Fatalf("tags = %q, want a (unchanged)", got)
		}
	})

	t.Run("no tag edits is a no-op", func(t *testing.T) {
		b := newTestBundle()
		if applyBundleTagEdits(b, bundleEdits{}) {
			t.Fatal("expected modified=false")
		}
	})
}

func TestApplyBundleFragmentEdits(t *testing.T) {
	t.Run("adds new fragment with placeholder + status line", func(t *testing.T) {
		b := newTestBundle()
		var buf bytes.Buffer
		w := iox.NewErrWriter(&buf)
		if !applyBundleFragmentEdits(b, bundleEdits{AddFragments: []string{"f1"}}, w) {
			t.Fatal("expected modified=true")
		}
		if _, ok := b.Fragments["f1"]; !ok {
			t.Fatal("fragment f1 should exist")
		}
		if !strings.Contains(buf.String(), "Added fragment: f1") {
			t.Fatalf("missing status line:\n%s", buf.String())
		}
	})

	t.Run("duplicate add is informational, not a modification", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Fragments: map[string]bundles.BundleFragment{"f1": {Content: "x"}}}
		var buf bytes.Buffer
		w := iox.NewErrWriter(&buf)
		if applyBundleFragmentEdits(b, bundleEdits{AddFragments: []string{"f1"}}, w) {
			t.Fatal("duplicate add should not report modified")
		}
		if !strings.Contains(buf.String(), "already exists: f1") {
			t.Fatalf("missing dup notice:\n%s", buf.String())
		}
	})

	t.Run("remove present, absent-remove is informational", func(t *testing.T) {
		b := &bundles.Bundle{Name: "b", Fragments: map[string]bundles.BundleFragment{"f1": {Content: "x"}}}
		var buf bytes.Buffer
		w := iox.NewErrWriter(&buf)
		mod := applyBundleFragmentEdits(b, bundleEdits{RemoveFragments: []string{"f1", "ghost"}}, w)
		if !mod {
			t.Fatal("removing present f1 should report modified")
		}
		if _, ok := b.Fragments["f1"]; ok {
			t.Fatal("f1 should be gone")
		}
		out := buf.String()
		if !strings.Contains(out, "Removed fragment: f1") || !strings.Contains(out, "not found: ghost") {
			t.Fatalf("unexpected output:\n%s", out)
		}
	})
}

func TestApplyBundleMCPEdits(t *testing.T) {
	b := newTestBundle()
	var buf bytes.Buffer
	w := iox.NewErrWriter(&buf)
	if !applyBundleMCPEdits(b, bundleEdits{AddMCP: []string{"srv"}}, w) {
		t.Fatal("expected modified=true")
	}
	if b.MCP["srv"].Command != "srv" {
		t.Fatalf("mcp srv command = %q, want srv", b.MCP["srv"].Command)
	}
	if !strings.Contains(buf.String(), "Added MCP server: srv") {
		t.Fatalf("missing status line:\n%s", buf.String())
	}
}

// applyBundleEdits ORs all categories — verify a single tag edit plus a fragment
// add both land and the aggregate result is true.
func TestApplyBundleEdits_Aggregates(t *testing.T) {
	b := newTestBundle()
	var buf bytes.Buffer
	w := iox.NewErrWriter(&buf)
	mod := applyBundleEdits(b, bundleEdits{
		Description:  "desc",
		AddTags:      []string{"t1"},
		AddFragments: []string{"f1"},
	}, w)
	if !mod {
		t.Fatal("expected modified=true")
	}
	if b.Description != "desc" || len(b.Tags) != 1 || len(b.Fragments) != 1 {
		t.Fatalf("not all categories applied: %+v", b)
	}
}

func TestApplyBundleEdits_NoEditsIsFalse(t *testing.T) {
	b := newTestBundle()
	var buf bytes.Buffer
	w := iox.NewErrWriter(&buf)
	if applyBundleEdits(b, bundleEdits{}, w) {
		t.Fatal("empty edits should report modified=false")
	}
}

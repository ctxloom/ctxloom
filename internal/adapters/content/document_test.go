package content

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// companionLoadout is the shape a companion's probe would produce: one bundle's
// worth of hooks and mcp servers, as bytes, with no filesystem anywhere.
func companionLoadout() map[BundleID]DocumentBundle {
	return map[BundleID]DocumentBundle{
		"ltk": {
			"hooks/pre_tool/guardrail.yaml": []byte("matcher: Bash\ntype: command\ncommand: ltk hook\n"),
			"mcp/ltk.yaml":                  []byte("command: ltk\nargs:\n  - mcp\n"),
			"mcp/.ltk.meta.yaml":            []byte("notes: shipped by the ltk companion\n"),
			"fragments/tool-discipline.md":  []byte("---\ntags:\n  - tools\n---\nUse the narrowest tool.\n"),
		},
	}
}

// TestDocumentStore_ReadsABundleWithNoFilesystem is the companion case: the same
// model over a different transport, decoding through every registered type with no
// second format and no special-casing.
func TestDocumentStore_ReadsABundleWithNoFilesystem(t *testing.T) {
	ctx := context.Background()
	store, err := NewDocumentStore(companionLoadout(), Provenance{RepoURL: "ctxloom:companion@ltk"})
	if err != nil {
		t.Fatalf("NewDocumentStore: %v", err)
	}
	ids, err := store.Bundles(ctx)
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	if !slices.Equal(ids, []BundleID{"ltk"}) {
		t.Fatalf("Bundles = %v", ids)
	}
	bundle, err := store.Open(ctx, "ltk")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(ctx)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	var keys []string
	for _, r := range refs {
		keys = append(keys, r.Key())
	}
	want := []string{
		"ltk#fragments/tool-discipline",
		"ltk#hooks/pre_tool/guardrail",
		"ltk#mcp/ltk",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("Refs = %v, want %v", keys, want)
	}
	// Provenance is stamped, so a companion item is neither local nor builtin and
	// therefore still gates — it does not inherit local auto-allow.
	for _, r := range refs {
		if r.IsLocal || r.RepoURL != "ctxloom:companion@ltk" {
			t.Errorf("%s: provenance = %+v", r.Key(), r)
		}
	}

	// Decoding, the sidecar, and the digest all work identically to the tree.
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "ltk", Kind: trust.KindMCP, Name: "ltk"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, trust.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	mcp, err := As[MCP](ctx, form)
	if err != nil {
		t.Fatalf("As[MCP]: %v", err)
	}
	if mcp.Command != "ltk" || mcp.Notes == "" {
		t.Errorf("mcp = %+v", mcp)
	}
	components, err := form.Components(ctx)
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	if got := componentPaths(components); !slices.Equal(got, []string{"mcp/.ltk.meta.yaml", "mcp/ltk.yaml"}) {
		t.Fatalf("Components = %v", got)
	}
}

// TestDocumentStore_IsReadOnly: a probed companion has nothing to write back to, so
// the type must not satisfy Writer even accidentally.
func TestDocumentStore_IsReadOnly(t *testing.T) {
	store, err := NewDocumentStore(companionLoadout(), Provenance{RepoURL: "ctxloom:companion@ltk"})
	if err != nil {
		t.Fatalf("NewDocumentStore: %v", err)
	}
	if _, isWriter := any(store).(Writer); isWriter {
		t.Error("DocumentStore implements Writer; a probed companion is read-only by construction")
	}
}

func TestDocumentStore_RefusesUnusableInput(t *testing.T) {
	for name, in := range map[string]map[BundleID]DocumentBundle{
		"traversal path":  {"b": {"../escape.md": []byte("x")}},
		"absolute path":   {"b": {"/etc/passwd": []byte("x")}},
		"newline in path": {"b": {"a\nb.md": []byte("x")}},
		"empty bundle":    {"b": {}},
		"bad bundle id":   {"../b": {"mcp/x.yaml": []byte("command: x\n")}},
	} {
		if _, err := NewDocumentStore(in, Provenance{IsLocal: true}); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.Is(err, ErrBadPath) && !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrBadPath or ErrNotFound", name, err)
		}
	}
	if _, err := NewDocumentStore(companionLoadout(), Provenance{}); err == nil {
		t.Error("unspecified provenance accepted")
	}
}

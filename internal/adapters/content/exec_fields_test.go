package content

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// requireEveryFieldSet fails when a fixture leaves a field at its zero value,
// so a field added to the surface type cannot ride through the round-trip
// tests below unexercised: the fixture must set it, and then the round trip
// proves the tree carries it.
func requireEveryFieldSet(t *testing.T, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	for i := 0; i < rv.NumField(); i++ {
		if rv.Field(i).IsZero() {
			t.Fatalf("fixture %T leaves %s unset; set it so the round trip covers it", v, rv.Type().Field(i).Name)
		}
	}
}

// putAndReadBack writes s at ref and decodes it back from the tree, returning
// the decoded surface and the components the item is made of on disk.
func putAndReadBack(t *testing.T, ref trust.Ref, s Surface) (Surface, []Component) {
	t.Helper()
	ctx := context.Background()
	store := emptyStore(t)
	if err := store.Put(ctx, ref, signing.FormRaw, s); err != nil {
		t.Fatalf("Put: %v", err)
	}
	bundle, err := store.Open(ctx, BundleID(ref.Bundle))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	got, err := item.Surface(ctx)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	return got, mustComponents(t, item)
}

// TestWriter_MCPRoundTripsEveryField: every field of an MCP server — the
// remote target (url, headers), the session-endpoint routing (served_by) and
// the tags — survives a write to the tree and a read back. URL and headers
// are MCP-client configuration and live in the content file; tags are ours
// and live in the sidecar, so the content file stays consumable as-is.
func TestWriter_MCPRoundTripsEveryField(t *testing.T) {
	want := MCP{
		Name:         "remote-tools",
		Command:      "srv",
		Args:         []string{"--flag"},
		Env:          map[string]string{"A": "1"},
		URL:          "https://mcp.example.com/mcp",
		Headers:      map[string]string{"Authorization": "Bearer ${TOKEN}"},
		ServedBy:     "session-endpoint",
		Tags:         []string{"ctxloom:link_id=pg"},
		Notes:        "notes",
		Installation: "install",
	}
	requireEveryFieldSet(t, want)
	got, comps := putAndReadBack(t, trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: want.Name, IsLocal: true}, want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MCP did not round-trip:\n got  %#v\n want %#v", got, want)
	}
	for _, c := range comps {
		body := string(c.Bytes)
		switch c.Path {
		case "mcp/remote-tools.yaml":
			for _, key := range []string{"url:", "headers:", "served_by:"} {
				if !strings.Contains(body, key) {
					t.Errorf("content file lacks %q:\n%s", key, body)
				}
			}
			if strings.Contains(body, "tags:") {
				t.Errorf("content file carries our tags key; it must stay pure MCP-client config:\n%s", body)
			}
		case "mcp/.remote-tools.meta.yaml":
			if !strings.Contains(body, "tags:") {
				t.Errorf("sidecar lacks tags:\n%s", body)
			}
		}
	}
}

// TestWriter_HookRoundTripsEveryField: every field of a hook, tags included,
// survives a write to the tree and a read back, and tags live in the sidecar
// beside order rather than in the behavioural content file.
func TestWriter_HookRoundTripsEveryField(t *testing.T) {
	want := Hook{
		Event:           "session_start",
		Name:            "guard",
		Order:           intp(HookOrderStep),
		Matcher:         "Bash",
		Type:            "command",
		Command:         "ctxloom",
		Args:            []string{"hook", "session-bind"},
		Prompt:          "prompt",
		Timeout:         5,
		Async:           true,
		PreToolFallback: true,
		Tags:            []string{"ctxloom:link_id=pg"},
	}
	requireEveryFieldSet(t, want)
	got, comps := putAndReadBack(t, trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: want.refName(), IsLocal: true}, want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Hook did not round-trip:\n got  %#v\n want %#v", got, want)
	}
	content, meta := splitHookComponents(comps)
	if content == nil || meta == nil {
		t.Fatalf("want a content file and a sidecar, got %v", componentPaths(comps))
	}
	if strings.Contains(string(content.Bytes), "tags:") {
		t.Errorf("hook content file carries our tags key:\n%s", content.Bytes)
	}
	if !strings.Contains(string(meta.Bytes), "tags:") {
		t.Errorf("hook sidecar lacks tags:\n%s", meta.Bytes)
	}
}

// TestWriter_RePutWithEmptiedMetadataDropsTheSidecar: a re-Put replaces the
// item. When the new encoding carries no sidecar (every one of our keys
// cleared), the old sidecar must not survive to be read back as the old tags.
func TestWriter_RePutWithEmptiedMetadataDropsTheSidecar(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "b", Kind: trust.KindMCP, Name: "srv", IsLocal: true}
	if err := store.Put(ctx, ref, signing.FormRaw, MCP{Name: "srv", Command: "c", Tags: []string{"old"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := MCP{Name: "srv", Command: "c"}
	if err := store.Put(ctx, ref, signing.FormRaw, want); err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	bundle, err := store.Open(ctx, "b")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	got, err := item.Surface(ctx)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("re-Put kept stale metadata:\n got  %#v\n want %#v", got, want)
	}
	for _, c := range mustComponents(t, item) {
		if IsMetaPath(c.Path) {
			t.Errorf("stale sidecar %s survived the re-Put", c.Path)
		}
	}
}

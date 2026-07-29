// This file is deliberately in the EXTERNAL test package. The point of the test
// below is that a third-party kind plugs into the content layer through the
// PUBLIC API alone, so it must not be able to reach an unexported helper. If this
// file ever needs to move into package content, the registry design has failed and
// the extension point is not real.
package content_test

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/content"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// widgetKind is a third-party item kind. It is a plain trust.ItemKind value whose
// Dir() falls through to the string itself, so registering it requires NO change to
// the trust package and no new constant anywhere in production code.
const widgetKind trust.ItemKind = "widgets"

// Widget is the third-party surface. It uses a dot-prefixed sidecar for its
// metadata, exercising the same grouping the shipped kinds rely on.
type Widget struct {
	Name  string
	Spec  string
	Owner string
}

func (Widget) Kind() trust.ItemKind { return widgetKind }

// TrustKind opts the kind INTO the trust gate. A third-party kind that should not
// be gated simply omits this method.
func (Widget) TrustKind() trust.ItemKind { return widgetKind }

type widgetType struct{}

func (widgetType) Name() string { return "widgets" }
func (widgetType) Dir() string  { return widgetKind.Dir() }

// widgetName recovers the item name from a candidate group's paths, using only
// content.IsMetaPath and the standard library.
func widgetName(src content.Source) (string, bool) {
	paths, err := src.List()
	if err != nil {
		return "", false
	}
	name := ""
	for _, p := range paths {
		if content.IsMetaPath(p) {
			continue
		}
		rel, ok := strings.CutPrefix(p, "widgets/")
		if !ok || strings.Contains(rel, "/") || path.Ext(rel) != ".widget" {
			return "", false
		}
		if name != "" {
			return "", false
		}
		name = strings.TrimSuffix(rel, ".widget")
	}
	if name == "" {
		return "", false
	}
	return name, true
}

func (widgetType) Detect(src content.Source) bool {
	_, ok := widgetName(src)
	return ok
}

func (t widgetType) Forms(src content.Source) ([]signing.Form, error) {
	if _, ok := widgetName(src); !ok {
		return nil, fmt.Errorf("not a widget")
	}
	return []signing.Form{signing.FormExec}, nil
}

func (t widgetType) RefFor(bundle string, src content.Source) (trust.Ref, error) {
	name, ok := widgetName(src)
	if !ok {
		return trust.Ref{}, fmt.Errorf("not a widget")
	}
	return trust.Ref{Bundle: bundle, Kind: widgetKind, Name: name}, nil
}

func (t widgetType) Decode(src content.Source) (content.Surface, error) {
	name, ok := widgetName(src)
	if !ok {
		return nil, fmt.Errorf("not a widget")
	}
	w := Widget{Name: name}
	paths, err := src.List()
	if err != nil {
		return nil, err
	}
	for _, p := range paths {
		data, err := src.Open(p)
		if err != nil {
			return nil, err
		}
		if content.IsMetaPath(p) {
			var meta struct {
				Owner string `yaml:"owner"`
			}
			if err := yaml.Unmarshal(data, &meta); err != nil {
				return nil, err
			}
			w.Owner = meta.Owner
			continue
		}
		w.Spec = string(data)
	}
	return w, nil
}

func (t widgetType) Encode(s content.Surface) ([]content.Component, error) {
	w, ok := s.(Widget)
	if !ok {
		return nil, fmt.Errorf("not a Widget")
	}
	contentPath := "widgets/" + w.Name + ".widget"
	out := []content.Component{{Path: contentPath, Mode: content.ModeRegular, Bytes: []byte(w.Spec)}}
	if w.Owner != "" {
		out = append(out, content.Component{
			Path:  content.MetaPath(contentPath),
			Mode:  content.ModeRegular,
			Bytes: []byte("owner: " + w.Owner + "\n"),
		})
	}
	return out, nil
}

// TestRegistryExtension_ThirdPartyKindWorksThroughPublicAPI is the registry
// extension test the design turns on: a kind registered FROM A TEST FILE must
// enumerate, resolve, decode, digest, sign and write through the public API with
// ZERO production changes.
func TestRegistryExtension_ThirdPartyKindWorksThroughPublicAPI(t *testing.T) {
	content.Register(widgetType{})
	t.Cleanup(func() { content.Unregister("widgets") })

	ctx := context.Background()
	fsys := afero.NewMemMapFs()
	root := "/store"
	write := func(p, body string) {
		t.Helper()
		if err := afero.WriteFile(fsys, root+"/"+p, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", p, err)
		}
	}
	write("gadgets/widgets/sprocket.widget", "teeth: 12\n")
	write("gadgets/widgets/.sprocket.meta.yaml", "owner: tools-team\n")
	write("gadgets/widgets/cog.widget", "teeth: 8\n")

	store, err := content.NewTreeStore(fsys, root, content.Provenance{IsLocal: true})
	if err != nil {
		t.Fatalf("NewTreeStore: %v", err)
	}
	bundle, err := store.Open(ctx, "gadgets")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Enumeration, including the kind filter, works for the new kind.
	refs, err := bundle.Refs(ctx, widgetKind)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	var keys []string
	for _, r := range refs {
		keys = append(keys, r.Key())
	}
	if !slices.Equal(keys, []string{"gadgets#widgets/cog", "gadgets#widgets/sprocket"}) {
		t.Fatalf("Refs = %v", keys)
	}

	// Resolution and typed decoding work, sidecar included.
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "gadgets", Kind: widgetKind, Name: "sprocket"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormExec)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	widget, err := content.As[Widget](ctx, form)
	if err != nil {
		t.Fatalf("As[Widget]: %v", err)
	}
	if widget.Name != "sprocket" || widget.Spec != "teeth: 12\n" || widget.Owner != "tools-team" {
		t.Fatalf("decoded widget = %+v", widget)
	}

	// The digest covers the dot-prefixed sidecar for a third-party kind too — the
	// grouping rule is in the walker, not in any kind's code.
	digest, err := form.Content(ctx)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	for _, want := range []string{"widgets/.sprocket.meta.yaml", "widgets/sprocket.widget"} {
		if !strings.Contains(string(digest), want) {
			t.Errorf("digest is missing %q:\n%s", want, digest)
		}
	}

	// Writing and signing work through the same interfaces.
	newRef := trust.Ref{Bundle: "gadgets", Kind: widgetKind, Name: "flange"}
	if err := store.Put(ctx, newRef, signing.FormExec, Widget{Name: "flange", Spec: "teeth: 3\n", Owner: "me"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := store.PutSignature(ctx, newRef, signing.FormExec, content.Namespace(signing.NamespacePublish), []byte("sig")); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	newItem, err := bundle.Item(ctx, newRef)
	if err != nil {
		t.Fatalf("Item(flange): %v", err)
	}
	newForm, err := newItem.Form(ctx, signing.FormExec)
	if err != nil {
		t.Fatalf("Form(flange): %v", err)
	}
	sigs, err := newForm.Signatures(ctx)
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if len(sigs) != 1 || string(sigs[0].Bytes) != "sig" {
		t.Fatalf("Signatures = %+v", sigs)
	}
	if err := store.Delete(ctx, newRef); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ok, _ := afero.Exists(fsys, root+"/gadgets/widgets/.flange.meta.yaml"); ok {
		t.Error("the third-party kind's sidecar survived Delete")
	}
}

// TestRegistry_RefusesDuplicateRegistration: a silently-ignored registration would
// make a whole kind vanish from every enumeration with no diagnostic.
func TestRegistry_RefusesDuplicateRegistration(t *testing.T) {
	content.Register(widgetType{})
	t.Cleanup(func() { content.Unregister("widgets") })
	defer func() {
		if recover() == nil {
			t.Error("registering the same kind twice did not panic")
		}
	}()
	content.Register(widgetType{})
}

func TestRegistry_ShippedTypesAreDiscoverable(t *testing.T) {
	var dirs []string
	for _, ty := range content.Types() {
		dirs = append(dirs, ty.Dir())
	}
	want := []string{"fragments", "hooks", "mcp", "profiles", "prompts", "skills"}
	if !slices.Equal(dirs, want) {
		t.Fatalf("Types() dirs = %v, want %v", dirs, want)
	}
	for _, k := range []trust.ItemKind{
		trust.KindFragment, trust.KindPrompt, trust.KindMCP, trust.KindHook, trust.KindSkill, content.KindProfile,
	} {
		if _, ok := content.TypeForKind(k); !ok {
			t.Errorf("no type registered for kind %q", k)
		}
	}
}

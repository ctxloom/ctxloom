package content

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// intp is a local helper: Hook.Order is a POINTER so that "no order declared" is
// distinguishable from "order 0", and a test that could not say the difference
// would pass against a design that had lost it.
func intp(v int) *int { return &v }

// TestHookOrder_SortsByDeclaredOrderNotByName is the load-bearing assertion of
// the whole ordering change. Names now carry IDENTITY ONLY, so alphabetical name
// order and execution order are free to disagree — and a tree whose hooks are the
// right bytes in the wrong order is BYTE-IDENTICAL to a correct one.
//
// Twelve hooks, because a width-or-string-sort bug only shows once the numbers
// pass nine: with a naive lexical sort "1000" precedes "200".
func TestHookOrder_SortsByDeclaredOrderNotByName(t *testing.T) {
	// Names chosen so ALPHABETICAL order is the exact REVERSE of declared order.
	var hooks []Hook
	for i := 0; i < 12; i++ {
		hooks = append(hooks, Hook{
			Event: "pre_tool",
			Name:  string(rune('z' - i)), // "z", "y", "x", ... descending
			Order: intp((i + 1) * HookOrderStep),
		})
	}

	shuffled := []Hook{hooks[7], hooks[0], hooks[11], hooks[3], hooks[9], hooks[1],
		hooks[5], hooks[10], hooks[2], hooks[8], hooks[4], hooks[6]}
	SortHooks(shuffled)

	var got []string
	for _, h := range shuffled {
		got = append(got, h.Name)
	}
	want := []string{"z", "y", "x", "w", "v", "u", "t", "s", "r", "q", "p", "o"}
	if !slices.Equal(got, want) {
		t.Fatalf("SortHooks = %v, want %v (declared order, NOT name order)", got, want)
	}

	// And prove the trap is real: name order would have given the opposite.
	byName := append([]string(nil), got...)
	sort.Strings(byName)
	if slices.Equal(byName, want) {
		t.Fatal("test is not discriminating: name order and declared order agree")
	}
}

// TestHookOrder_AbsentSortsAfterEveryDeclaredOrder pins what an ABSENT order
// means for a hook authored before the field existed: it made NO claim, so it
// must not silently jump ahead of a hook that did. Sorting it first would be a
// silent reorder of exactly the kind this field exists to prevent; sorting it
// last preserves the pure-APPEND semantics hooks already had.
func TestHookOrder_AbsentSortsAfterEveryDeclaredOrder(t *testing.T) {
	hooks := []Hook{
		{Event: "pre_tool", Name: "aaa-no-order"},
		{Event: "pre_tool", Name: "zzz-ordered", Order: intp(900000)},
	}
	SortHooks(hooks)
	if hooks[0].Name != "zzz-ordered" {
		t.Fatalf("order = %q first; a hook with a DECLARED order must precede one with none", hooks[0].Name)
	}
}

// TestHookOrder_TiesBreakByName: equal orders are a legitimate "I don't care"
// statement, not an error. Resolution must still be TOTAL and stable, or the same
// tree enumerates differently on two machines. Name is the tie-break because it is
// the one value guaranteed unique within an event — it is the filename.
func TestHookOrder_TiesBreakByName(t *testing.T) {
	hooks := []Hook{
		{Event: "pre_tool", Name: "beta", Order: intp(100)},
		{Event: "pre_tool", Name: "alpha", Order: intp(100)},
		{Event: "pre_tool", Name: "delta"},
		{Event: "pre_tool", Name: "charlie"},
	}
	SortHooks(hooks)
	var got []string
	for _, h := range hooks {
		got = append(got, h.Name)
	}
	want := []string{"alpha", "beta", "charlie", "delta"}
	if !slices.Equal(got, want) {
		t.Fatalf("SortHooks = %v, want %v (ties by name, absent orders last and also by name)", got, want)
	}
}

// TestHook_OrderLivesInTheSidecarNotTheContentFile pins residency. `order` is
// OURS, not the hook's behavioural configuration, and encodeExecItem's rule is
// that our keys never pollute a content file. Equally load-bearing: the sidecar
// is a COMPONENT, so it is hashed — an order change stales that hook's own
// countersignature, which is correct, rather than riding along unattested.
func TestHook_OrderLivesInTheSidecarNotTheContentFile(t *testing.T) {
	comps := encodeHook(t, Hook{
		Event: "pre_tool", Name: "guard", Type: "command", Command: "echo hi", Order: intp(300),
	})
	if len(comps) != 2 {
		t.Fatalf("components = %v, want a content file and an order sidecar", componentPaths(comps))
	}
	content, meta := splitHookComponents(comps)
	if content == nil || meta == nil {
		t.Fatalf("components = %v, want one content file and one sidecar", componentPaths(comps))
	}
	if content.Path != "hooks/pre_tool/guard.yaml" {
		t.Errorf("content path = %q", content.Path)
	}
	if meta.Path != "hooks/pre_tool/.guard.meta.yaml" {
		t.Errorf("sidecar path = %q", meta.Path)
	}
	if strings.Contains(string(content.Bytes), "order:") {
		t.Errorf("the content file carries our `order` key:\n%s", content.Bytes)
	}
	if !strings.Contains(string(meta.Bytes), "order: 300") {
		t.Errorf("sidecar does not carry the order:\n%s", meta.Bytes)
	}
}

// TestHook_NoOrderWritesNoSidecar: absence must be represented by the ABSENCE of a
// file, not by a sidecar saying nothing. An empty `{}` sidecar per hook would be
// bytes in the digest that mean nothing, and would make "authored before the
// field" indistinguishable from "deliberately unordered".
func TestHook_NoOrderWritesNoSidecar(t *testing.T) {
	comps := encodeHook(t, Hook{Event: "pre_tool", Name: "guard", Type: "command", Command: "x"})
	if len(comps) != 1 {
		t.Fatalf("components = %v, want only the content file", componentPaths(comps))
	}
}

// TestHook_OrderRoundTripsThroughTheTree is the both-directions assertion: the
// order survives encode → filesystem → walk → decode. A field that encodes but
// does not decode is this project's characteristic bug — it looks honoured and is
// silently ignored.
func TestHook_OrderRoundTripsThroughTheTree(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/hooks/pre_tool/.guard.meta.yaml", "order: 700\n")

	item := mustHookItem(t, store, "code-quality", "pre_tool/guard")
	surf, err := item.Surface(ctx)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	h, ok := surf.(Hook)
	if !ok {
		t.Fatalf("surface is %T, want Hook", surf)
	}
	if h.Order == nil || *h.Order != 700 {
		t.Fatalf("Order = %v, want 700 — the sidecar was hashed but not read", h.Order)
	}

	// And the sidecar must be a COMPONENT of the item, or its bytes are in the
	// tree, outside the digest, explained by nothing.
	comps := mustComponents(t, item)
	if _, meta := splitHookComponents(comps); meta == nil {
		t.Fatalf("components = %v, want the order sidecar among them", componentPaths(comps))
	}
}

// --- helpers ----------------------------------------------------------------

func encodeHook(t *testing.T, h Hook) []Component {
	t.Helper()
	comps, err := hookType{}.Encode(h)
	if err != nil {
		t.Fatalf("Encode(%s): %v", h.Name, err)
	}
	return comps
}

// splitHookComponents separates an item's content file from its metadata
// sidecar. Either may be nil, which is itself the assertion in several tests.
func splitHookComponents(comps []Component) (content, meta *Component) {
	for i := range comps {
		if IsMetaPath(comps[i].Path) {
			meta = &comps[i]
		} else {
			content = &comps[i]
		}
	}
	return content, meta
}

func mustHookItem(t *testing.T, store *TreeStore, bundleID BundleID, name string) Item {
	t.Helper()
	ctx := context.Background()
	bundle, err := store.Open(ctx, bundleID)
	if err != nil {
		t.Fatalf("Open(%s): %v", bundleID, err)
	}
	item, err := bundle.Item(ctx, trust.Ref{Bundle: string(bundleID), Kind: trust.KindHook, Name: name})
	if err != nil {
		t.Fatalf("Item(%s): %v", name, err)
	}
	return item
}

func mustComponents(t *testing.T, item Item) []Component {
	t.Helper()
	ctx := context.Background()
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	comps, err := form.Components(ctx)
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	return comps
}

// TestHook_InsertingAHookLeavesItsNeighboursBytesUntouched is the property the
// whole change exists for. Under the retired `<NN>-<slug>` scheme, inserting a
// hook renumbered every hook below it: their FILENAMES changed, their bytes moved,
// and their countersignatures staled for a change they did not make.
//
// With order as data and SPARSE spacing, an insert writes exactly one new file and
// one new sidecar. Nothing else in the event is touched.
func TestHook_InsertingAHookLeavesItsNeighboursBytesUntouched(t *testing.T) {
	before := []Hook{
		{Event: "pre_tool", Name: "guard", Type: "command", Command: "guard", Order: intp(100)},
		{Event: "pre_tool", Name: "audit", Type: "command", Command: "audit", Order: intp(200)},
	}
	// Insert between them WITHOUT touching either: the gap absorbs it.
	inserted := Hook{Event: "pre_tool", Name: "stamp", Type: "command", Command: "stamp", Order: intp(150)}

	encode := func(h Hook) map[string]string {
		out := map[string]string{}
		for _, c := range encodeHook(t, h) {
			out[c.Path] = string(c.Bytes)
		}
		return out
	}

	baseline := map[string]map[string]string{}
	for _, h := range before {
		baseline[h.Name] = encode(h)
	}
	after := append(append([]Hook(nil), before...), inserted)
	SortHooks(after)

	if got := []string{after[0].Name, after[1].Name, after[2].Name}; !slices.Equal(got, []string{"guard", "stamp", "audit"}) {
		t.Fatalf("resolved order = %v, want [guard stamp audit]", got)
	}
	for _, h := range before {
		now := encode(h)
		for p, b := range baseline[h.Name] {
			if now[p] != b {
				t.Errorf("inserting a hook changed %q for neighbour %q — positional identity is back", p, h.Name)
			}
		}
		if len(now) != len(baseline[h.Name]) {
			t.Errorf("neighbour %q gained or lost a component on insert", h.Name)
		}
	}
}

package content

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

func TestTreeStore_Bundles(t *testing.T) {
	store := fixtureStore(t)
	got, err := store.Bundles(context.Background())
	if err != nil {
		t.Fatalf("Bundles: %v", err)
	}
	want := []BundleID{"code-quality", "tooling"}
	if !slices.Equal(got, want) {
		t.Fatalf("Bundles = %v, want %v", got, want)
	}
}

// TestBundle_RefsEnumeratesEveryKind is the whole-format assertion: the exact set
// of items the fixture tree contains, for every registered kind, with hooks
// carrying NAME identity.
//
// It also pins the two things that must NOT appear: a metadata sidecar as an item
// of its own (".postgres.meta"), and a distilled form as a second item — a form
// selects which FILE is exposed, and Ref carries no form component.
func TestBundle_RefsEnumeratesEveryKind(t *testing.T) {
	store := fixtureStore(t)
	bundle, err := store.Open(context.Background(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(context.Background())
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, r.Key())
	}
	want := []string{
		"code-quality#fragments/solid",
		"code-quality#fragments/tricky",
		"code-quality#hooks/pre_tool/audit",
		"code-quality#hooks/pre_tool/guard",
		"code-quality#hooks/session_start/greet",
		"code-quality#mcp/postgres",
		"code-quality#profiles/strict",
		"code-quality#prompts/review",
		"code-quality#skills/code-reviewer",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Refs =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	for _, r := range refs {
		if !r.IsLocal {
			t.Errorf("%s: provenance not stamped by the store", r.Key())
		}
	}
}

// TestBundle_RefsDoesNotEnumerateSidecarsAsItems states the trap separately from
// the golden list, because it is the one that fails silently: a sidecar listed as
// an item would ALSO mean it is not a component of its item, so its bytes would
// leave the digest and declared executability would stop being attested.
func TestBundle_RefsDoesNotEnumerateSidecarsAsItems(t *testing.T) {
	store := fixtureStore(t)
	bundle, err := store.Open(context.Background(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(context.Background())
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	for _, r := range refs {
		if strings.Contains(r.Name, ".meta") || strings.HasPrefix(r.Name, ".") {
			t.Errorf("sidecar enumerated as an item: %s", r.Key())
		}
		if strings.Contains(r.Name, ".distilled") {
			t.Errorf("a form enumerated as an item: %s", r.Key())
		}
	}
}

func TestBundle_RefsKindFilter(t *testing.T) {
	store := fixtureStore(t)
	bundle, err := store.Open(context.Background(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(context.Background(), trust.KindHook, trust.KindMCP)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, r.Key())
	}
	want := []string{
		"code-quality#hooks/pre_tool/audit",
		"code-quality#hooks/pre_tool/guard",
		"code-quality#hooks/session_start/greet",
		"code-quality#mcp/postgres",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Refs = %v, want %v", got, want)
	}
}

func TestBundle_ItemResolvesByPath(t *testing.T) {
	store := fixtureStore(t)
	bundle, err := store.Open(context.Background(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	refs, err := bundle.Refs(context.Background())
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	for _, ref := range refs {
		item, err := bundle.Item(context.Background(), ref)
		if err != nil {
			t.Fatalf("Item(%s): %v", ref.Key(), err)
		}
		if item.Ref().Key() != ref.Key() {
			t.Errorf("Item(%s).Ref() = %s", ref.Key(), item.Ref().Key())
		}
	}
}

func TestBundle_ItemNotFound(t *testing.T) {
	store := fixtureStore(t)
	bundle, err := store.Open(context.Background(), "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for name, ref := range map[string]trust.Ref{
		"missing fragment":   {Bundle: "code-quality", Kind: trust.KindFragment, Name: "nope"},
		"sidecar as item":    {Bundle: "code-quality", Kind: trust.KindMCP, Name: ".postgres.meta"},
		"hook without event": {Bundle: "code-quality", Kind: trust.KindHook, Name: "guard"},
	} {
		if _, err := bundle.Item(context.Background(), ref); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := store.Open(context.Background(), "no-such-bundle"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open(missing) err = %v, want ErrNotFound", err)
	}
}

// ------------------------------------------------------------------ forms

func TestItem_FormsReportExactlyWhatExists(t *testing.T) {
	store := fixtureStore(t)
	bundle, _ := store.Open(context.Background(), "code-quality")
	for _, tc := range []struct {
		ref  trust.Ref
		want []signing.Form
	}{
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid"}, []signing.Form{signing.FormRaw, signing.FormDistilled}},
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "tricky"}, []signing.Form{signing.FormRaw}},
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindPrompt, Name: "review"}, []signing.Form{signing.FormRaw}},
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"}, []signing.Form{signing.FormRaw}},
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: "pre_tool/guard"}, []signing.Form{signing.FormRaw}},
		{trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "code-reviewer"}, []signing.Form{signing.FormRaw}},
		{trust.Ref{Bundle: "code-quality", Kind: KindProfile, Name: "strict"}, []signing.Form{signing.FormRaw}},
	} {
		item, err := bundle.Item(context.Background(), tc.ref)
		if err != nil {
			t.Fatalf("Item(%s): %v", tc.ref.Key(), err)
		}
		got, err := item.Forms(context.Background())
		if err != nil {
			t.Fatalf("Forms(%s): %v", tc.ref.Key(), err)
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: Forms = %v, want %v", tc.ref.Key(), got, tc.want)
		}
	}
}

// TestItem_ExecutableSurfacesCarryOnlyTheBaseForm states the layout invariant on
// its own: an executable surface has exactly ONE materialization, carried by an
// unsuffixed filename, so it reports the BASE layout form and nothing else.
//
// The ROLE that distinguishes it from a fragment lives in the composite
// attestation form the trust layer derives from the item's kind, NOT in this
// axis — so an mcp server reporting the same layout form a never-distilled
// document reports is correct, and a distilled form it does not have is still
// refused.
func TestItem_ExecutableSurfacesCarryOnlyTheBaseForm(t *testing.T) {
	store := fixtureStore(t)
	bundle, _ := store.Open(context.Background(), "code-quality")
	for _, ref := range []trust.Ref{
		{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"},
		{Bundle: "code-quality", Kind: trust.KindHook, Name: "pre_tool/audit"},
	} {
		item, err := bundle.Item(context.Background(), ref)
		if err != nil {
			t.Fatalf("Item(%s): %v", ref.Key(), err)
		}
		if _, err := item.Form(context.Background(), signing.FormRaw); err != nil {
			t.Errorf("%s: FormRaw: %v", ref.Key(), err)
		}
		if _, err := item.Form(context.Background(), signing.FormDistilled); !errors.Is(err, ErrNoSuchForm) {
			t.Errorf("%s: FormDistilled err = %v, want ErrNoSuchForm", ref.Key(), err)
		}
	}
}

func TestItem_MissingFormIsRefused(t *testing.T) {
	store := fixtureStore(t)
	bundle, _ := store.Open(context.Background(), "code-quality")
	item, err := bundle.Item(context.Background(), trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "tricky"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	if _, err := item.Form(context.Background(), signing.FormDistilled); !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("err = %v, want ErrNoSuchForm for a never-distilled fragment", err)
	}
}

// TestForm_RawAndDistilledAreIndependent is the storage-model proof that blessing
// the raw form cannot validate a distilled exposure: the two forms are separate
// files with separate component sets, so their digests differ and their signature
// lookups cannot collide. Nothing enforces this in code — it falls out.
func TestForm_RawAndDistilledAreIndependent(t *testing.T) {
	store := fixtureStore(t)
	ctx := context.Background()
	bundle, _ := store.Open(ctx, "code-quality")
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid"}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	raw, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form(raw): %v", err)
	}
	distilled, err := item.Form(ctx, signing.FormDistilled)
	if err != nil {
		t.Fatalf("Form(distilled): %v", err)
	}

	rawComponents, err := raw.Components(ctx)
	if err != nil {
		t.Fatalf("raw.Components: %v", err)
	}
	distComponents, err := distilled.Components(ctx)
	if err != nil {
		t.Fatalf("distilled.Components: %v", err)
	}
	if got := componentPaths(rawComponents); !slices.Equal(got, []string{"fragments/solid.md"}) {
		t.Errorf("raw components = %v", got)
	}
	if got := componentPaths(distComponents); !slices.Equal(got, []string{"fragments/solid.distilled.md"}) {
		t.Errorf("distilled components = %v", got)
	}

	rawDigest, err := raw.Content(ctx)
	if err != nil {
		t.Fatalf("raw.Content: %v", err)
	}
	distDigest, err := distilled.Content(ctx)
	if err != nil {
		t.Fatalf("distilled.Content: %v", err)
	}
	if bytes.Equal(rawDigest, distDigest) {
		t.Fatal("raw and distilled produced the same Content digest")
	}

	// Sign only the raw form; the distilled form must see nothing.
	if err := store.PutSignature(ctx, ref, signing.FormRaw, Namespace(signing.NamespacePublish), []byte("sig-over-raw")); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	rawSigs, err := raw.Signatures(ctx)
	if err != nil {
		t.Fatalf("raw.Signatures: %v", err)
	}
	if len(rawSigs) != 1 || string(rawSigs[0].Bytes) != "sig-over-raw" {
		t.Fatalf("raw.Signatures = %+v", rawSigs)
	}
	distSigs, err := distilled.Signatures(ctx)
	if err != nil {
		t.Fatalf("distilled.Signatures: %v", err)
	}
	if len(distSigs) != 0 {
		t.Fatalf("a raw signature was found for the distilled form: %+v", distSigs)
	}
}

// TestForm_ContentIsAlwaysADigestEvenAtN1 pins the uniform poly-file rule: a
// single-file item is N=1, not a special case, so Content is the manifest and
// never the file bytes.
func TestForm_ContentIsAlwaysADigestEvenAtN1(t *testing.T) {
	store := fixtureStore(t)
	ctx := context.Background()
	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "tricky"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	components, err := form.Components(ctx)
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	if len(components) != 1 {
		t.Fatalf("want a single component, got %v", componentPaths(components))
	}
	digest, err := form.Content(ctx)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if bytes.Equal(digest, components[0].Bytes) {
		t.Fatal("Content returned raw bytes at N=1 instead of a digest")
	}
	if !bytes.HasPrefix(digest, []byte(DigestVersionMarker)) {
		t.Fatalf("Content is not a digest: %q", digest)
	}
}

// TestForm_ContentIsDeterministic covers the three axes the design calls out:
// repeated reads, on-disk write order, and a real content change.
func TestForm_ContentIsDeterministic(t *testing.T) {
	ctx := context.Background()
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "code-reviewer"}

	read := func(store *TreeStore) []byte {
		t.Helper()
		bundle, err := store.Open(ctx, "code-quality")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		item, err := bundle.Item(ctx, ref)
		if err != nil {
			t.Fatalf("Item: %v", err)
		}
		form, err := item.Form(ctx, signing.FormRaw)
		if err != nil {
			t.Fatalf("Form: %v", err)
		}
		digest, err := form.Content(ctx)
		if err != nil {
			t.Fatalf("Content: %v", err)
		}
		return digest
	}

	first := fixtureStore(t)
	a, b := read(first), read(first)
	if !bytes.Equal(a, b) {
		t.Fatalf("repeated reads of one tree differ:\n%s\n---\n%s", a, b)
	}
	// A second tree with the SAME contents written in REVERSE order — the
	// "component reorder on disk must not change the digest" requirement. Nothing
	// in the digest may depend on creation order, directory-read order, or mtime.
	second := reverseOrderFixtureStore(t)
	if got := read(second); !bytes.Equal(a, got) {
		t.Fatalf("writing the same tree in reverse order changed the digest:\n%s\n---\n%s", a, got)
	}
	// And any content change MUST change it.
	writeFile(t, second.fsys, fixtureRoot+"/code-quality/skills/code-reviewer/scripts/run.sh", "#!/bin/sh\necho changed\n")
	if got := read(second); bytes.Equal(a, got) {
		t.Fatal("a changed component did not change the digest")
	}
}

// ---------------------------------------------------------------- the trap

// TestSkill_DotPrefixedSidecarIsHashedAndAttestsExecutability is the required
// security-trap test.
//
// Dotfiles are excluded by default in much glob and walk code. If the walker
// misses ".code-reviewer.meta.yaml", it never enters the digest, and because
// executability is DECLARED in that sidecar rather than read from a mode bit,
// executability silently stops being attested — while every signature still
// verifies and everything looks green. So: the sidecar must appear in
// Components(), it must change Content(), and its declaration must reach
// Component.Mode.
func TestSkill_DotPrefixedSidecarIsHashedAndAttestsExecutability(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "code-reviewer"}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	components, err := form.Components(ctx)
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	paths := componentPaths(components)
	want := []string{
		"skills/.code-reviewer.meta.yaml",
		"skills/code-reviewer/SKILL.md",
		"skills/code-reviewer/references/checklist.md",
		"skills/code-reviewer/scripts/run.sh",
	}
	if !slices.Equal(paths, want) {
		t.Fatalf("Components =\n  %s\nwant\n  %s", strings.Join(paths, "\n  "), strings.Join(want, "\n  "))
	}

	// The digest must name the sidecar.
	digest, err := form.Content(ctx)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !strings.Contains(string(digest), "skills/.code-reviewer.meta.yaml") {
		t.Fatalf("the dot-prefixed sidecar is missing from the digest:\n%s", digest)
	}

	// The declaration must reach Component.Mode — with the FIXTURE's own file
	// modes irrelevant, since the declaration is the source of truth.
	byPath := map[string]ComponentMode{}
	for _, c := range components {
		byPath[c.Path] = c.Mode
	}
	if got := byPath["skills/code-reviewer/scripts/run.sh"]; got != ModeExecutable {
		t.Errorf("scripts/run.sh mode = %q, want %q (declared in the sidecar)", got, ModeExecutable)
	}
	if got := byPath["skills/code-reviewer/SKILL.md"]; got != ModeRegular {
		t.Errorf("SKILL.md mode = %q, want %q", got, ModeRegular)
	}

	// Editing ONLY the sidecar — dropping the executable declaration — must
	// change Content. If it does not, executability is unattested.
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/skills/.code-reviewer.meta.yaml",
		"tags:\n  - review\nnotes: Wraps the house review checklist.\n")
	item2, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item after sidecar edit: %v", err)
	}
	form2, err := item2.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form after sidecar edit: %v", err)
	}
	digest2, err := form2.Content(ctx)
	if err != nil {
		t.Fatalf("Content after sidecar edit: %v", err)
	}
	if bytes.Equal(digest, digest2) {
		t.Fatal("dropping the executable declaration did not change Content — executability is not attested")
	}
	components2, err := form2.Components(ctx)
	if err != nil {
		t.Fatalf("Components after sidecar edit: %v", err)
	}
	for _, c := range components2 {
		if c.Path == "skills/code-reviewer/scripts/run.sh" && c.Mode != ModeRegular {
			t.Errorf("mode = %q after the declaration was removed, want %q", c.Mode, ModeRegular)
		}
	}
}

// TestMCP_SidecarIsHashedAndContentFileStaysPure covers the same trap for an
// executable surface, and the property the sidecar exists for: the content file
// carries nothing of ours.
func TestMCP_SidecarIsHashedAndContentFileStaysPure(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	components, err := form.Components(ctx)
	if err != nil {
		t.Fatalf("Components: %v", err)
	}
	if got := componentPaths(components); !slices.Equal(got, []string{"mcp/.postgres.meta.yaml", "mcp/postgres.yaml"}) {
		t.Fatalf("Components = %v", got)
	}
	for _, c := range components {
		if c.Path != "mcp/postgres.yaml" {
			continue
		}
		for _, ours := range []string{"notes:", "installation:", "content_hash:", "name:"} {
			if strings.Contains(string(c.Bytes), ours) {
				t.Errorf("the mcp content file carries our key %q — it must stay pure vendor config:\n%s", ours, c.Bytes)
			}
		}
	}
	digest, err := form.Content(ctx)
	if err != nil {
		t.Fatalf("Content: %v", err)
	}
	if !strings.Contains(string(digest), "mcp/.postgres.meta.yaml") {
		t.Fatalf("sidecar missing from the digest:\n%s", digest)
	}
}

// ------------------------------------------------------------------ hooks

// TestHook_TwoHooksInOneEventHaveNameIdentity is the ordering/identity case the
// old ordinal scheme got wrong: two hooks in one event, identified by NAME, with
// order carried as declared metadata rather than as position.
func TestHook_TwoHooksInOneEventHaveNameIdentity(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")

	for _, tc := range []struct {
		name    string
		command string
	}{
		{"pre_tool/guard", "ltk guard"},
		{"pre_tool/audit", "audit-log record"},
	} {
		ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: tc.name}
		item, err := bundle.Item(ctx, ref)
		if err != nil {
			t.Fatalf("Item(%s): %v", tc.name, err)
		}
		form, err := item.Form(ctx, signing.FormRaw)
		if err != nil {
			t.Fatalf("Form(%s): %v", tc.name, err)
		}
		hook, err := As[Hook](ctx, form)
		if err != nil {
			t.Fatalf("As[Hook](%s): %v", tc.name, err)
		}
		event, name, _ := strings.Cut(tc.name, "/")
		if hook.Event != event || hook.Name != name {
			t.Errorf("%s: decoded as event=%q name=%q", tc.name, hook.Event, hook.Name)
		}
		if hook.Command != tc.command {
			t.Errorf("%s: Command = %q, want %q", tc.name, hook.Command, tc.command)
		}
		// Neither name nor order may appear in the hook's CONTENT file: keeping
		// them out is what lets existing hook approvals and content-rejections
		// survive the identity change with no preimage contract bump.
		components, err := form.Components(ctx)
		if err != nil {
			t.Fatalf("Components(%s): %v", tc.name, err)
		}
		// A hook has NO metadata of its own, so it has exactly one component and
		// no sidecar; and neither the name nor an ordinal may appear in it.
		if len(components) != 1 {
			t.Errorf("%s: components = %v, want exactly the content file", tc.name, componentPaths(components))
		}
		for _, c := range components {
			for _, forbidden := range []string{"name:", "order:", "event:", "index:"} {
				if strings.Contains(string(c.Bytes), forbidden) {
					t.Errorf("%s: content file carries %q:\n%s", tc.name, forbidden, c.Bytes)
				}
			}
		}
	}
}

// TestHook_SingleHookInAnEventResolvesIdentically guards the walk's one genuine
// ambiguity: an event directory holding exactly one hook is offered to the type as
// a directory candidate AND, if declined, as a file group one level down. Both
// paths must yield the same ref, and the item must be enumerated exactly once.
func TestHook_SingleHookInAnEventResolvesIdentically(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")
	refs, err := bundle.Refs(ctx, trust.KindHook)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	count := 0
	for _, r := range refs {
		if r.Name == "session_start/greet" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("session_start/greet enumerated %d times, want exactly 1 (refs=%v)", count, refs)
	}
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: "session_start/greet"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	hook, err := As[Hook](ctx, form)
	if err != nil {
		t.Fatalf("As[Hook]: %v", err)
	}
	if hook.Event != "session_start" || hook.Name != "greet" || !hook.PreToolFallback {
		t.Errorf("decoded hook = %+v", hook)
	}
}

// --------------------------------------------------------------- surfaces

func TestSurfaces_DecodeAuthoredFields(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")

	formFor := func(ref trust.Ref, f signing.Form) Form {
		t.Helper()
		item, err := bundle.Item(ctx, ref)
		if err != nil {
			t.Fatalf("Item(%s): %v", ref.Key(), err)
		}
		form, err := item.Form(ctx, f)
		if err != nil {
			t.Fatalf("Form(%s): %v", ref.Key(), err)
		}
		return form
	}

	frag, err := As[Fragment](ctx, formFor(trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid"}, signing.FormRaw))
	if err != nil {
		t.Fatalf("As[Fragment]: %v", err)
	}
	if frag.Name != "solid" || !slices.Equal(frag.Tags, []string{"go", "design"}) {
		t.Errorf("fragment = %+v", frag)
	}
	if !strings.Contains(frag.Body, "Single responsibility") || strings.Contains(frag.Body, "tags:") {
		t.Errorf("fragment body = %q", frag.Body)
	}
	if frag.Distilled == "" || frag.DistilledBy != "test-model-1" {
		t.Errorf("distilled form not carried onto the surface: %+v", frag)
	}
	if frag.ContentHash == "" {
		t.Error("content_hash not carried verbatim")
	}

	tricky, err := As[Fragment](ctx, formFor(trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "tricky"}, signing.FormRaw))
	if err != nil {
		t.Fatalf("As[Fragment]: %v", err)
	}
	if !tricky.NoDistill {
		t.Error("no_distill not decoded")
	}
	if !strings.Contains(tricky.Body, "{{ not_a_template }}") || strings.Count(tricky.Body, "---") != 2 {
		t.Errorf("body with rules and mustaches corrupted: %q", tricky.Body)
	}

	cmd, err := As[Command](ctx, formFor(trust.Ref{Bundle: "code-quality", Kind: trust.KindPrompt, Name: "review"}, signing.FormRaw))
	if err != nil {
		t.Fatalf("As[Command]: %v", err)
	}
	if cmd.Description == "" || cmd.Installation == "" {
		t.Errorf("command = %+v", cmd)
	}
	// Per-engine keys are TYPED and readable, not an opaque passthrough.
	cc, ok := cmd.Exports.For("claude-code")
	if !ok {
		t.Fatalf("claude-code exports missing: %+v", cmd.Exports)
	}
	if !cc.IsEnabled() || cc.Description != "Review the staged diff" || cc.ArgumentHint != "[path]" ||
		!slices.Equal(cc.AllowedTools, []string{"Read", "Grep"}) || cc.Model != "sonnet" {
		t.Errorf("claude-code export = %+v", cc)
	}
	if cmd.Exports.IsEnabledFor("codex") {
		t.Error("codex export is explicitly disabled but read as enabled")
	}
	if cmd.Exports.IsEnabledFor("engine-with-no-settings") != true {
		t.Error("an engine with no declared settings must be enabled (opt-out model)")
	}
	// An engine this build has never heard of is carried, not dropped: an older
	// ctxloom must not silently discard a newer bundle's configuration.
	if fut, ok := cmd.Exports.For("some-future-engine"); !ok || fut.Description == "" {
		t.Errorf("unknown engine's settings were dropped: %+v", cmd.Exports)
	}

	mcp, err := As[MCP](ctx, formFor(trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"}, signing.FormRaw))
	if err != nil {
		t.Fatalf("As[MCP]: %v", err)
	}
	if mcp.Command != "mcp-postgres" || len(mcp.Env) != 3 || mcp.Notes == "" || mcp.Installation == "" {
		t.Errorf("mcp = %+v", mcp)
	}

	skill, err := As[Skill](ctx, formFor(trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "code-reviewer"}, signing.FormRaw))
	if err != nil {
		t.Fatalf("As[Skill]: %v", err)
	}
	if len(skill.Files) != 3 || skill.Notes == "" {
		t.Errorf("skill = %+v", skill)
	}
	if !skill.Exports.IsEnabledFor("claude-code") || skill.Exports.IsEnabledFor("kiro") {
		t.Errorf("skill per-engine enablement = %+v", skill.Exports)
	}
	var execCount int
	for _, f := range skill.Files {
		if f.Mode == ModeExecutable {
			execCount++
			if f.Path != "scripts/run.sh" {
				t.Errorf("unexpected executable file %q", f.Path)
			}
		}
	}
	if execCount != 1 {
		t.Errorf("executable files = %d, want 1", execCount)
	}
}

// TestProfile_PriorityOrderingRoundTrips pins the ordering the design says must
// survive: profiles.FragmentRef round-trips priority losslessly, including the
// bare-string form at priority 0 and negative priorities.
func TestProfile_PriorityOrderingRoundTrips(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")
	ref := trust.Ref{Bundle: "code-quality", Kind: KindProfile, Name: "strict"}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	profile, err := As[Profile](ctx, form)
	if err != nil {
		t.Fatalf("As[Profile]: %v", err)
	}
	if profile.Name != "strict" || profile.Def.Name != "strict" {
		t.Errorf("profile name = %q / %q", profile.Name, profile.Def.Name)
	}
	if len(profile.Def.Fragments) != 3 {
		t.Fatalf("fragments = %+v", profile.Def.Fragments)
	}
	for i, want := range []struct {
		name     string
		priority int
	}{{"solid", 0}, {"tricky", 10}, {"solid", -5}} {
		got := profile.Def.Fragments[i]
		if got.Name != want.name || got.Priority != want.priority {
			t.Errorf("fragment %d = %+v, want %s@%d", i, got, want.name, want.priority)
		}
	}
	if !slices.Equal(profile.Def.Parents, []string{"base"}) {
		t.Errorf("parents = %v", profile.Def.Parents)
	}

	// Re-encode and re-decode: order and priorities must survive verbatim.
	pt, ok := TypeForKind(KindProfile)
	if !ok {
		t.Fatal("no profile type registered")
	}
	encoded, err := pt.Encode(profile)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := pt.Decode(newMemSource(encoded))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	roundTripped, ok := back.(Profile)
	if !ok {
		t.Fatalf("Decode returned %T", back)
	}
	if !slices.Equal(roundTripped.Def.Fragments, profile.Def.Fragments) {
		t.Errorf("fragment ordering lost on round-trip:\n got %+v\nwant %+v", roundTripped.Def.Fragments, profile.Def.Fragments)
	}
}

// TestProfile_IsNotTrustGated makes the structural fact a test: participation in
// the trust gate is an optional interface, and a profile does not implement it.
func TestProfile_IsNotTrustGated(t *testing.T) {
	if _, gated := any(Profile{}).(TrustGated); gated {
		t.Error("Profile implements TrustGated; profiles are not trust-gated")
	}
	for _, s := range []Surface{Fragment{}, Command{}, MCP{}, Hook{}, Skill{}} {
		tg, gated := s.(TrustGated)
		if !gated {
			t.Errorf("%T does not implement TrustGated", s)
			continue
		}
		if tg.TrustKind() != s.Kind() {
			t.Errorf("%T: TrustKind %q != Kind %q", s, tg.TrustKind(), s.Kind())
		}
	}
}

func TestAs_WrongTypeIsRefused(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid"})
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormRaw)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	if _, err := As[MCP](ctx, form); !errors.Is(err, ErrSurfaceType) {
		t.Fatalf("err = %v, want ErrSurfaceType", err)
	}
}

// TestWalk_MalformedDirectoriesFailLoud is the fail-closed half of the candidate
// walk: a skill directory with no SKILL.md, a stray file with a foreign
// extension, an item at the wrong depth. None of these may be misread as an
// item — and none may be SILENTLY DROPPED either, which is what the walk used to
// do. A dropped file is a file no manifest ever covers and no diagnostic ever
// mentions, so a mis-extensioned hook vanishes and an added one rides along.
func TestWalk_MalformedDirectoriesFailLoud(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct{ path, body string }{
		"skill directory with no descriptor": {"skills/broken/notes.md", "no descriptor here\n"},
		"skill nested a level too deep":      {"skills/nested/inner/SKILL.md", "---\nname: inner\n---\nbody\n"},
		"foreign extension in a kind dir":    {"fragments/README.txt", "not a fragment\n"},
		"mcp server a level too deep":        {"mcp/subdir/thing.yaml", "command: x\n"},
	} {
		t.Run(name, func(t *testing.T) {
			store := emptyStore(t)
			writeFile(t, store.fsys, fixtureRoot+"/code-quality/"+tc.path, tc.body)
			bundle, err := store.Open(ctx, "code-quality")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			refs, err := bundle.Refs(ctx)
			if err == nil {
				t.Fatalf("Refs accepted a tree containing %s, returning %d refs", tc.path, len(refs))
			}
			if !errors.Is(err, ErrUnclaimed) {
				t.Fatalf("err = %v, want ErrUnclaimed", err)
			}
			if !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("error %q does not name the offending file %q", err, tc.path)
			}
		})
	}
}

func TestNewTreeStore_RefusesAmbiguousProvenance(t *testing.T) {
	for name, prov := range map[string]Provenance{
		"local and builtin": {IsLocal: true, IsBuiltin: true},
		"local with url":    {IsLocal: true, RepoURL: "https://example.test/x"},
		"unspecified":       {},
	} {
		if _, err := NewTreeStore(newMemFsWithRoot(t), fixtureRoot, prov); err == nil {
			t.Errorf("%s: NewTreeStore accepted %+v", name, prov)
		}
	}
}

// TestDecode_RefusesUnexplainedSidecars: a sidecar in a kind whose metadata does
// NOT live in a sidecar is grouped into the item and hashed, so ignoring it would
// mean bytes ride along under a valid signature that no decode path accounts for.
func TestDecode_RefusesUnexplainedSidecars(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	root := fixtureRoot + "/code-quality"
	writeFile(t, store.fsys, root+"/fragments/.solid.meta.yaml", "tags:\n  - smuggled\n")
	writeFile(t, store.fsys, root+"/profiles/.strict.meta.yaml", "owner: nobody\n")

	bundle, _ := store.Open(ctx, "code-quality")
	for name, ref := range map[string]trust.Ref{
		"fragment": {Bundle: "code-quality", Kind: trust.KindFragment, Name: "solid"},
		"profile":  {Bundle: "code-quality", Kind: KindProfile, Name: "strict"},
		// Hooks are deliberately NOT in this table any more: `order` gave them
		// legitimate metadata, so their sidecar is explained. That it decodes
		// rather than being refused is pinned by
		// TestHook_OrderRoundTripsThroughTheTree.
	} {
		item, err := bundle.Item(ctx, ref)
		if err != nil {
			t.Fatalf("%s: Item: %v", name, err)
		}
		if _, err := item.Surface(ctx); err == nil {
			t.Errorf("%s: an unexplained sidecar was silently accepted", name)
		}
	}
}

// TestEngineExports_EncodeIsDeterministic pins the one library behaviour the map
// fields depend on: yaml.v3 must emit map keys in a stable order. Determinism is a
// hard requirement of the digest, and EngineExports (like MCP.Env) is a map, so a
// dependency bump that made map emission order-dependent would silently break
// every signature. This fails loudly if that ever changes.
func TestEngineExports_EncodeIsDeterministic(t *testing.T) {
	exports := EngineExports{
		"opencode":    {Description: "o"},
		"claude-code": {Description: "c"},
		"kiro":        {Description: "k"},
		"codex":       {Description: "x"},
		"antigravity": {Description: "a"},
	}
	first, err := marshalYAML(skillMeta{Exports: exports})
	if err != nil {
		t.Fatalf("marshalYAML: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := marshalYAML(skillMeta{Exports: exports})
		if err != nil {
			t.Fatalf("marshalYAML: %v", err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("map encoding is not deterministic:\n%s\n---\n%s", first, again)
		}
	}
}

// TestMCPEnv_EncodeIsDeterministic is the same guard for the other map field.
func TestMCPEnv_EncodeIsDeterministic(t *testing.T) {
	env := map[string]string{"PGHOST": "h", "PGPORT": "5432", "PGDATABASE": "d", "PGUSER": "u"}
	mt, _ := TypeForKind(trust.KindMCP)
	first, err := mt.Encode(MCP{Name: "pg", Command: "c", Env: env})
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := mt.Encode(MCP{Name: "pg", Command: "c", Env: env})
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if !bytes.Equal(first[0].Bytes, again[0].Bytes) {
			t.Fatalf("env encoding is not deterministic:\n%s\n---\n%s", first[0].Bytes, again[0].Bytes)
		}
	}
}

// TestSurfaceType_MetaResidencyIsPerType pins what each shipped type DECLARES,
// so a residency change is a visible diff rather than a surprise in a digest.
func TestSurfaceType_MetaResidencyIsPerType(t *testing.T) {
	for _, tc := range []struct {
		kind        trust.ItemKind
		wantSidecar bool
		wantPath    string
	}{
		{trust.KindFragment, false, ""},
		{trust.KindPrompt, false, ""},
		// A hook DOES keep a sidecar: `order` is ctxloom's key, not the hook's
		// behavioural config, so encodeExecItem's purity rule puts it beside the
		// content file rather than in it.
		{trust.KindHook, true, "hooks/.postgres.meta.yaml"},
		{KindProfile, false, ""},
		{trust.KindMCP, true, "mcp/.postgres.meta.yaml"},
		{trust.KindSkill, true, "skills/.postgres.meta.yaml"},
	} {
		ty, ok := TypeForKind(tc.kind)
		if !ok {
			t.Fatalf("no type for %q", tc.kind)
		}
		got, hasMeta := ty.Meta().PathFor(ty.Dir(), "postgres")
		if hasMeta != tc.wantSidecar {
			t.Errorf("%s: PathFor ok = %v, want %v", tc.kind, hasMeta, tc.wantSidecar)
		}
		if got != tc.wantPath {
			t.Errorf("%s: PathFor = %q, want %q", tc.kind, got, tc.wantPath)
		}
		// Accepts must agree with PathFor: a type that stores no metadata file
		// must not accept one, which is what makes a stray sidecar loud.
		if accepts := ty.Meta().Accepts(ty.Dir(), MetaPathForName(ty.Dir(), "postgres")); accepts != tc.wantSidecar {
			t.Errorf("%s: Accepts = %v, want %v", tc.kind, accepts, tc.wantSidecar)
		}
	}
}

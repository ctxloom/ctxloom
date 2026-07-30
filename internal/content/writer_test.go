package content

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestWriter_FragmentRoundTrip writes both forms of a fragment and reads them
// back: the decoded surface must equal what was written, and the stored bytes must
// be exactly what Encode produced.
func TestWriter_FragmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "written", IsLocal: true}
	want := Fragment{
		Name:         "written",
		Tags:         []string{"alpha", "beta"},
		Notes:        "note",
		Installation: "install me",
		ContentHash:  "sha256:abc",
		Body:         "Body with a rule\n\n---\n\nand {{ mustaches }}.\n",
		Distilled:    "Short version.\n",
		DistilledBy:  "test-model-2",
	}
	if err := store.Put(ctx, ref, signing.FormRaw, want); err != nil {
		t.Fatalf("Put(raw): %v", err)
	}
	if err := store.Put(ctx, ref, signing.FormDistilled, want); err != nil {
		t.Fatalf("Put(distilled): %v", err)
	}

	bundle, err := store.Open(ctx, "code-quality")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	forms, err := item.Forms(ctx)
	if err != nil {
		t.Fatalf("Forms: %v", err)
	}
	if !slices.Equal(forms, []signing.Form{signing.FormRaw, signing.FormDistilled}) {
		t.Fatalf("Forms = %v", forms)
	}
	surface, err := item.Surface(ctx)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	got, ok := surface.(Fragment)
	if !ok {
		t.Fatalf("Surface returned %T", surface)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip lost data:\n got %+v\nwant %+v", got, want)
	}

	// Bytes on disk must be exactly Encode's output — no re-serialization drift.
	ft, _ := TypeForKind(trust.KindFragment)
	encoded, err := ft.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	byPath := map[string][]byte{}
	for _, f := range []signing.Form{signing.FormRaw, signing.FormDistilled} {
		form, err := item.Form(ctx, f)
		if err != nil {
			t.Fatalf("Form(%s): %v", f, err)
		}
		components, err := form.Components(ctx)
		if err != nil {
			t.Fatalf("Components(%s): %v", f, err)
		}
		for _, c := range components {
			byPath[c.Path] = c.Bytes
		}
	}
	for _, c := range encoded {
		stored, ok := byPath[c.Path]
		if !ok {
			t.Errorf("%s was never written", c.Path)
			continue
		}
		if !bytes.Equal(stored, c.Bytes) {
			t.Errorf("%s: stored bytes differ from Encode output:\n got %q\nwant %q", c.Path, stored, c.Bytes)
		}
	}
}

// TestWriter_PutOneFormLeavesTheOtherAlone: forms are separate files, so writing
// one must not touch the other's bytes or its signature.
func TestWriter_PutOneFormLeavesTheOtherAlone(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "solo"}
	original := Fragment{Name: "solo", Body: "raw body\n", Distilled: "distilled body\n"}
	if err := store.Put(ctx, ref, signing.FormRaw, original); err != nil {
		t.Fatalf("Put(raw): %v", err)
	}
	if err := store.Put(ctx, ref, signing.FormDistilled, original); err != nil {
		t.Fatalf("Put(distilled): %v", err)
	}
	rawBefore, err := afero.ReadFile(store.fsys, fixtureRoot+"/code-quality/fragments/solo.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	revised := original
	revised.Distilled = "a different distillation\n"
	if err := store.Put(ctx, ref, signing.FormDistilled, revised); err != nil {
		t.Fatalf("Put(distilled): %v", err)
	}
	rawAfter, err := afero.ReadFile(store.fsys, fixtureRoot+"/code-quality/fragments/solo.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Equal(rawBefore, rawAfter) {
		t.Fatalf("rewriting the distilled form changed the raw file:\n got %q\nwant %q", rawAfter, rawBefore)
	}
}

// TestWriter_DeleteRemovesContentAndSidecar covers the requirement explicitly: a
// sidecar left behind would be an orphan the walker still groups, and an item that
// looks deleted but is not.
func TestWriter_DeleteRemovesContentAndSidecar(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"}
	for _, p := range []string{
		fixtureRoot + "/code-quality/mcp/postgres.yaml",
		fixtureRoot + "/code-quality/mcp/.postgres.meta.yaml",
	} {
		if ok, _ := afero.Exists(store.fsys, p); !ok {
			t.Fatalf("fixture is missing %s", p)
		}
	}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, p := range []string{
		fixtureRoot + "/code-quality/mcp/postgres.yaml",
		fixtureRoot + "/code-quality/mcp/.postgres.meta.yaml",
	} {
		if ok, _ := afero.Exists(store.fsys, p); ok {
			t.Errorf("%s survived Delete", p)
		}
	}
	bundle, _ := store.Open(ctx, "code-quality")
	if _, err := bundle.Item(ctx, ref); !errors.Is(err, ErrNotFound) {
		t.Errorf("Item after Delete: err = %v, want ErrNotFound", err)
	}
}

// TestWriter_DeleteSkillRemovesThePackageAndItsSidecar: the sidecar lives OUTSIDE
// the package directory, so deleting the directory alone would leave it behind.
func TestWriter_DeleteSkillRemovesThePackageAndItsSidecar(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "code-reviewer"}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, p := range []string{
		fixtureRoot + "/code-quality/skills/.code-reviewer.meta.yaml",
		fixtureRoot + "/code-quality/skills/code-reviewer/SKILL.md",
		fixtureRoot + "/code-quality/skills/code-reviewer/scripts/run.sh",
	} {
		if ok, _ := afero.Exists(store.fsys, p); ok {
			t.Errorf("%s survived Delete", p)
		}
	}
}

// TestWriter_DeleteKeepsSiblingHooksInTheSameEvent: the event directory is shared,
// so pruning must not take a sibling with it.
func TestWriter_DeleteKeepsSiblingHooksInTheSameEvent(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	if err := store.Delete(ctx, trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: "pre_tool/guard"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	bundle, _ := store.Open(ctx, "code-quality")
	refs, err := bundle.Refs(ctx, trust.KindHook)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	var got []string
	for _, r := range refs {
		got = append(got, r.Name)
	}
	if !slices.Equal(got, []string{"pre_tool/audit", "session_start/greet"}) {
		t.Fatalf("hooks after Delete = %v", got)
	}
}

// TestWriter_DeleteDoesNotRemoveSignatures: signatures are keyed by content hash so
// they OUTLIVE the file. A rejection a file deletion could remove would mean you
// could un-blacklist content by deleting it.
func TestWriter_DeleteDoesNotRemoveSignatures(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"}
	if err := store.PutSignature(ctx, ref, signing.FormExec, Namespace(signing.NamespaceReject), []byte("rejection")); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	before, err := afero.ReadDir(store.fsys, fixtureRoot+"/code-quality/.sigs")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if err := store.Delete(ctx, ref); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, err := afero.ReadDir(store.fsys, fixtureRoot+"/code-quality/.sigs")
	if err != nil {
		t.Fatalf("ReadDir after Delete: %v", err)
	}
	if len(after) != len(before) || len(after) == 0 {
		t.Fatalf("signature store changed by Delete: %d -> %d entries", len(before), len(after))
	}
}

// TestWriter_PutSignatureRoundTrip covers write-then-read, per-namespace
// separation, idempotence for an identical signature, and coexistence of two
// different signatures over the same content (mixed provenance).
func TestWriter_PutSignatureRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: "pre_tool/guard"}
	publish := Namespace(signing.NamespacePublish)
	approve := Namespace(signing.NamespaceApprove)

	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormExec)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	sigs, err := form.Signatures(ctx)
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("unsigned item reported %d signatures", len(sigs))
	}

	if err := store.PutSignature(ctx, ref, signing.FormExec, publish, []byte("pub-a")); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	// Writing the same signature twice is idempotent: the filename derives from
	// the signature's own bytes.
	if err := store.PutSignature(ctx, ref, signing.FormExec, publish, []byte("pub-a")); err != nil {
		t.Fatalf("PutSignature (repeat): %v", err)
	}
	// A second, different publisher signature over the same content coexists.
	if err := store.PutSignature(ctx, ref, signing.FormExec, publish, []byte("pub-b")); err != nil {
		t.Fatalf("PutSignature (second signer): %v", err)
	}
	if err := store.PutSignature(ctx, ref, signing.FormExec, approve, []byte("approval")); err != nil {
		t.Fatalf("PutSignature (approve): %v", err)
	}

	sigs, err = form.Signatures(ctx)
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if len(sigs) != 3 {
		t.Fatalf("Signatures = %+v, want 3", sigs)
	}
	pub := sigs.ForNamespace(publish)
	if len(pub) != 2 {
		t.Errorf("ForNamespace(publish) = %d signatures, want 2", len(pub))
	}
	app := sigs.ForNamespace(approve)
	if len(app) != 1 || string(app[0]) != "approval" {
		t.Errorf("ForNamespace(approve) = %q", app)
	}
	// Ordering must be stable across reads.
	again, err := form.Signatures(ctx)
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if !reflect.DeepEqual(sigs, again) {
		t.Errorf("signature order is not stable:\n%+v\n%+v", sigs, again)
	}
}

// TestWriter_PutSignatureIsContentKeyed: editing the item's bytes must strand the
// old signature rather than have it apply to the new content.
func TestWriter_PutSignatureIsContentKeyed(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindHook, Name: "pre_tool/guard"}
	if err := store.PutSignature(ctx, ref, signing.FormExec, Namespace(signing.NamespacePublish), []byte("sig")); err != nil {
		t.Fatalf("PutSignature: %v", err)
	}
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/hooks/pre_tool/guard.yaml", "matcher: Bash\ntype: command\ncommand: rm -rf /\n")

	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	form, err := item.Form(ctx, signing.FormExec)
	if err != nil {
		t.Fatalf("Form: %v", err)
	}
	sigs, err := form.Signatures(ctx)
	if err != nil {
		t.Fatalf("Signatures: %v", err)
	}
	if len(sigs) != 0 {
		t.Fatalf("a signature over the old bytes was found for the new bytes: %+v", sigs)
	}
}

func TestWriter_PutRefusesMismatchedIdentity(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	for name, tc := range map[string]struct {
		ref     trust.Ref
		surface Surface
	}{
		"name disagrees with ref": {
			trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "expected"},
			Fragment{Name: "actual", Body: "x\n"},
		},
		"kind disagrees with ref": {
			trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "thing"},
			MCP{Name: "thing", Command: "x"},
		},
	} {
		if err := store.Put(ctx, tc.ref, signing.FormRaw, tc.surface); !errors.Is(err, ErrSurfaceType) {
			t.Errorf("%s: err = %v, want ErrSurfaceType", name, err)
		}
	}
}

func TestWriter_PutRefusesAFormTheSurfaceDoesNotCarry(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindFragment, Name: "plain"}
	err := store.Put(ctx, ref, signing.FormDistilled, Fragment{Name: "plain", Body: "only raw\n"})
	if !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("err = %v, want ErrNoSuchForm", err)
	}
}

// TestWriter_PutSkillAppliesDeclaredMode: the declaration drives the filesystem
// bit, not the other way round.
func TestWriter_PutSkillAppliesDeclaredMode(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "helper"}
	skill := Skill{
		Name:  "helper",
		Tags:  []string{"tools"},
		Notes: "note",
		Files: []SkillFile{
			{Path: "SKILL.md", Mode: ModeRegular, Bytes: []byte("---\nname: helper\ndescription: d\n---\nbody\n")},
			{Path: "scripts/go.sh", Mode: ModeExecutable, Bytes: []byte("#!/bin/sh\ntrue\n")},
		},
	}
	if err := store.Put(ctx, ref, signing.FormRaw, skill); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := store.fsys.Stat(fixtureRoot + "/code-quality/skills/helper/scripts/go.sh")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("declared-executable file written with mode %v", info.Mode().Perm())
	}
	bundle, _ := store.Open(ctx, "code-quality")
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	surface, err := item.Surface(ctx)
	if err != nil {
		t.Fatalf("Surface: %v", err)
	}
	got, ok := surface.(Skill)
	if !ok {
		t.Fatalf("Surface returned %T", surface)
	}
	if !reflect.DeepEqual(got, skill) {
		t.Fatalf("skill round-trip lost data:\n got %+v\nwant %+v", got, skill)
	}
}

func TestWriter_PutSignatureRefusesUnsafeNamespace(t *testing.T) {
	ctx := context.Background()
	store := fixtureStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindMCP, Name: "postgres"}
	for _, ns := range []Namespace{"", "../escape", "with/slash", "*"} {
		if err := store.PutSignature(ctx, ref, signing.FormExec, ns, []byte("sig")); !errors.Is(err, ErrBadPath) {
			t.Errorf("namespace %q: err = %v, want ErrBadPath", ns, err)
		}
	}
	if err := store.PutSignature(ctx, ref, signing.FormExec, Namespace(signing.NamespacePublish), nil); err == nil {
		t.Error("an empty signature was accepted")
	}
}

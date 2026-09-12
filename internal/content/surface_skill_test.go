package content

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// A skill's BODY is its descriptor. A package may carry one body per layout
// form — SKILL.md for the base form, SKILL.distilled.md for the distilled one —
// and materializing the package for an engine writes exactly the selected
// body at SKILL.md, the only place an engine reads.

var (
	rawBody       = []byte("---\nname: helper\ndescription: long\n---\nthe full body\n")
	distilledBody = []byte("---\nname: helper\ndescription: short\n---\nthe short body\n")
	helperScript  = []byte("#!/bin/sh\ntrue\n")
)

// twoBodySkill is a package carrying both bodies plus one sibling file.
func twoBodySkill() Skill {
	return Skill{
		Name: "helper",
		Files: []SkillFile{
			{Path: "SKILL.distilled.md", Mode: ModeRegular, Bytes: distilledBody},
			{Path: "SKILL.md", Mode: ModeRegular, Bytes: rawBody},
			{Path: "scripts/go.sh", Mode: ModeExecutable, Bytes: helperScript},
		},
	}
}

// oneBodySkill is a package carrying only the base body.
func oneBodySkill() Skill {
	return Skill{
		Name: "helper",
		Files: []SkillFile{
			{Path: "SKILL.md", Mode: ModeRegular, Bytes: rawBody},
			{Path: "scripts/go.sh", Mode: ModeExecutable, Bytes: helperScript},
		},
	}
}

func skillForms(t *testing.T, store *TreeStore, ref trust.Ref) []signing.Form {
	t.Helper()
	ctx := context.Background()
	bundle, err := store.Open(ctx, BundleID(ref.Bundle))
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
	return forms
}

func TestSkillType_Forms_ReportsEachBodyThePackageCarries(t *testing.T) {
	ctx := context.Background()
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "helper"}

	single := emptyStore(t)
	if err := single.Put(ctx, ref, signing.FormRaw, oneBodySkill()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, want := skillForms(t, single, ref), []signing.Form{signing.FormRaw}; !reflect.DeepEqual(got, want) {
		t.Errorf("one-body package forms = %v, want %v", got, want)
	}

	double := emptyStore(t)
	for _, f := range []signing.Form{signing.FormRaw, signing.FormDistilled} {
		if err := double.Put(ctx, ref, f, twoBodySkill()); err != nil {
			t.Fatalf("Put(%s): %v", f, err)
		}
	}
	if got, want := skillForms(t, double, ref), []signing.Form{signing.FormRaw, signing.FormDistilled}; !reflect.DeepEqual(got, want) {
		t.Errorf("two-body package forms = %v, want %v", got, want)
	}
}

// A sibling file whose name merely ends in the form suffix is not a body: only
// the descriptor carries a form. Without this, "references/notes.distilled.md"
// would make the package claim a distilled body it does not have.
//
// The sibling is planted on disk directly rather than through Put: a Forms that
// misread it as a body would make Put file it under the phantom form and never
// write it, and the misreading would then have nothing on disk to trip over.
func TestSkillType_Forms_OnlyTheDescriptorCarriesAForm(t *testing.T) {
	ctx := context.Background()
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "helper"}
	store := emptyStore(t)
	if err := store.Put(ctx, ref, signing.FormRaw, oneBodySkill()); err != nil {
		t.Fatalf("Put: %v", err)
	}
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/skills/helper/references/notes.distilled.md", "notes\n")
	if got, want := skillForms(t, store, ref), []signing.Form{signing.FormRaw}; !reflect.DeepEqual(got, want) {
		t.Errorf("forms = %v, want %v", got, want)
	}
}

func TestSkill_Materialize_WritesExactlyTheSelectedBodyAtTheDescriptor(t *testing.T) {
	for _, tc := range []struct {
		form signing.Form
		body []byte
	}{
		{signing.FormRaw, rawBody},
		{signing.FormDistilled, distilledBody},
	} {
		got, err := twoBodySkill().Materialize(tc.form)
		if err != nil {
			t.Fatalf("Materialize(%s): %v", tc.form, err)
		}
		want := []SkillFile{
			{Path: "SKILL.md", Mode: ModeRegular, Bytes: tc.body},
			{Path: "scripts/go.sh", Mode: ModeExecutable, Bytes: helperScript},
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Materialize(%s):\n got %+v\nwant %+v", tc.form, got, want)
		}
	}
}

// Selecting a body the package does not carry is refused — never an empty
// package, never a silent fallback to the body it does have.
func TestSkill_Materialize_AbsentFormIsALoudError(t *testing.T) {
	got, err := oneBodySkill().Materialize(signing.FormDistilled)
	if !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("err = %v, want ErrNoSuchForm", err)
	}
	if got != nil {
		t.Errorf("an absent form yielded files: %+v", got)
	}
}

func TestSkill_Materialize_RefusesAFormThatIsNotALayoutForm(t *testing.T) {
	if _, err := twoBodySkill().Materialize(signing.FormNone); !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("err = %v, want ErrNoSuchForm", err)
	}
}

// SkillMaterialization is the path-level rule Materialize applies; it is
// exported for the loader that holds a package as paths and bytes rather than
// as a Skill, so the two cannot disagree about which file is the body.
func TestSkillMaterialization_MapsTheSelectedBodyOntoTheDescriptor(t *testing.T) {
	paths := []string{"SKILL.distilled.md", "SKILL.md", "scripts/go.sh"}
	got, err := SkillMaterialization(paths, signing.FormDistilled)
	if err != nil {
		t.Fatalf("SkillMaterialization: %v", err)
	}
	want := map[string]string{"SKILL.distilled.md": "SKILL.md", "scripts/go.sh": "scripts/go.sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("distilled layout = %v, want %v", got, want)
	}
	got, err = SkillMaterialization(paths, signing.FormRaw)
	if err != nil {
		t.Fatalf("SkillMaterialization: %v", err)
	}
	want = map[string]string{"SKILL.md": "SKILL.md", "scripts/go.sh": "scripts/go.sh"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("raw layout = %v, want %v", got, want)
	}
	if _, err := SkillMaterialization([]string{"SKILL.md"}, signing.FormDistilled); !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("absent body: err = %v, want ErrNoSuchForm", err)
	}
}

func TestSkillForms_ReportsTheBaseFormFirstThenEachBodyPresent(t *testing.T) {
	if got, want := SkillForms([]string{"SKILL.md"}), []signing.Form{signing.FormRaw}; !reflect.DeepEqual(got, want) {
		t.Errorf("SkillForms = %v, want %v", got, want)
	}
	if got, want := SkillForms([]string{"scripts/go.sh", "SKILL.distilled.md", "SKILL.md"}), []signing.Form{signing.FormRaw, signing.FormDistilled}; !reflect.DeepEqual(got, want) {
		t.Errorf("SkillForms = %v, want %v", got, want)
	}
}

// The store partitions a two-body package by form exactly as it does a
// fragment: the distilled body is the distilled form's ONLY component, and the
// raw form is everything else. Put of one form leaves the other's file alone.
func TestWriter_PutSkillPartitionsBodiesByForm(t *testing.T) {
	ctx := context.Background()
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "helper"}
	pkg := fixtureRoot + "/code-quality/skills/helper/"

	store := emptyStore(t)
	if err := store.Put(ctx, ref, signing.FormDistilled, twoBodySkill()); err != nil {
		t.Fatalf("Put(distilled): %v", err)
	}
	if exists, _ := afero.Exists(store.fsys, pkg+"SKILL.md"); exists {
		t.Fatal("Put(distilled) wrote the raw body")
	}
	if exists, _ := afero.Exists(store.fsys, pkg+"scripts/go.sh"); exists {
		t.Fatal("Put(distilled) wrote a sibling file")
	}
	got, err := afero.ReadFile(store.fsys, pkg+"SKILL.distilled.md")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != string(distilledBody) {
		t.Errorf("distilled body = %q", got)
	}

	if err := store.Put(ctx, ref, signing.FormRaw, twoBodySkill()); err != nil {
		t.Fatalf("Put(raw): %v", err)
	}
	bundle, err := store.Open(ctx, BundleID(ref.Bundle))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	item, err := bundle.Item(ctx, ref)
	if err != nil {
		t.Fatalf("Item: %v", err)
	}
	for _, tc := range []struct {
		form signing.Form
		want []string
	}{
		{signing.FormRaw, []string{"skills/.helper.meta.yaml", "skills/helper/SKILL.md", "skills/helper/scripts/go.sh"}},
		{signing.FormDistilled, []string{"skills/helper/SKILL.distilled.md"}},
	} {
		form, err := item.Form(ctx, tc.form)
		if err != nil {
			t.Fatalf("Form(%s): %v", tc.form, err)
		}
		components, err := form.Components(ctx)
		if err != nil {
			t.Fatalf("Components(%s): %v", tc.form, err)
		}
		if got := componentPaths(components); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s components = %v, want %v", tc.form, got, tc.want)
		}
	}
}

// Put of a form the package does not carry is refused before anything is
// written — the writer's existing guard, pinned here for the skill kind.
func TestWriter_PutSkillRefusesAnAbsentBody(t *testing.T) {
	ctx := context.Background()
	store := emptyStore(t)
	ref := trust.Ref{Bundle: "code-quality", Kind: trust.KindSkill, Name: "helper"}
	err := store.Put(ctx, ref, signing.FormDistilled, oneBodySkill())
	if !errors.Is(err, ErrNoSuchForm) {
		t.Fatalf("err = %v, want ErrNoSuchForm", err)
	}
	if exists, _ := afero.DirExists(store.fsys, fixtureRoot+"/code-quality/skills"); exists {
		t.Fatal("a refused Put left files behind")
	}
}

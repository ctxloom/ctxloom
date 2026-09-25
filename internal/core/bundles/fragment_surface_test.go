package bundles

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
)

// The fragment surface is the MODEL of what an agent is shown of a fragment,
// and the trust preimage is computed FROM it. These tests state that property
// as an invariant over the struct — never as a hand-written list of fields,
// which would pass while stale.

// The single preimage builder frames exactly the surface's two presented
// values on the fragment contract: the premise and the body in the selected
// form. Nothing else on the fragment reaches the signed bytes.
func TestBundleFragment_ContentPayload_IsTheFramedSurface(t *testing.T) {
	frag := BundleFragment{
		ItemBody: ItemBody{
			Content:   "RAW-BYTES",
			Distilled: "DISTILLED-BYTES",
		},
		Premise: "when it applies",
	}

	rawPayload, rawForm := frag.ContentPayload(false)
	distPayload, distForm := frag.ContentPayload(true)

	assert.Equal(t, signing.FragmentPreimage("when it applies", []byte("RAW-BYTES")), rawPayload)
	assert.Equal(t, FormRaw, rawForm)
	assert.Equal(t, signing.FragmentPreimage("when it applies", []byte("DISTILLED-BYTES")), distPayload)
	assert.Equal(t, FormDistilled, distForm)

	// EffectiveContentHash must hash exactly these bytes — same function,
	// not a re-derivation.
	rawHash, rawHashForm := frag.EffectiveContentHash(false)
	distHash, distHashForm := frag.EffectiveContentHash(true)
	assert.Equal(t, hashContent(rawPayload), rawHash)
	assert.Equal(t, rawForm, rawHashForm)
	assert.Equal(t, hashContent(distPayload), distHash)
	assert.Equal(t, distForm, distHashForm)
}

// What the surface's getters present is byte-for-byte what its preimage
// frames: the body is the same bytes EffectiveContent serves in the same form,
// and the premise is the fragment's own.
func TestFragmentSurface_GettersAreWhatThePreimageFrames(t *testing.T) {
	frag := BundleFragment{
		ItemBody: ItemBody{
			Content:      "RAW-BYTES",
			Distilled:    "DISTILLED-BYTES",
			Notes:        "for humans",
			Installation: "brew install x",
		},
		Premise: "when it applies",
	}
	for _, prefer := range []bool{false, true} {
		s := frag.Surface(prefer)
		assert.Equal(t, "when it applies", s.Premise())
		assert.Equal(t, frag.EffectiveContent(prefer), s.Body())
		assert.Equal(t, signing.FragmentPreimage(s.Premise(), []byte(s.Body())), s.Preimage())
		payload, form := frag.ContentPayload(prefer)
		assert.Equal(t, payload, s.Preimage())
		assert.Equal(t, form, s.Form())
	}
	assert.Equal(t, FormRaw, frag.Surface(false).Form())
	assert.Equal(t, FormDistilled, frag.Surface(true).Form())
}

// THE SUPPRESSION ATTACK, stated at the layer review records approvals on:
// operations/review.go hashes ContentPayload. Rewriting a guardrail's premise
// to a condition that never holds — body untouched — must move that hash in
// EVERY form, so a previously granted approval stops matching and the item
// returns to pending instead of silently never loading.
func TestBundleFragment_PremiseRewriteInvalidatesEveryApprovalHash(t *testing.T) {
	guardrail := BundleFragment{
		ItemBody: ItemBody{
			Content:   "never force-remove a worktree",
			Distilled: "no --force on worktree remove",
		},
		Premise: "you are about to remove a worktree",
	}
	approvedRaw, _ := guardrail.EffectiveContentHash(false)
	approvedDistilled, _ := guardrail.EffectiveContentHash(true)

	suppressed := guardrail
	suppressed.Premise = "applies only when debugging Fortran"

	nowRaw, _ := suppressed.EffectiveContentHash(false)
	nowDistilled, _ := suppressed.EffectiveContentHash(true)
	assert.NotEqual(t, approvedRaw, nowRaw, "raw approval survived a premise rewrite")
	assert.NotEqual(t, approvedDistilled, nowDistilled, "distilled approval survived a premise rewrite")
}

// Adding a premise to a fragment that had none is a change to what the agent
// is shown (the fragment stops loading unconditionally), so it is a change to
// the preimage too. Absence is a value, not a missing field.
func TestBundleFragment_AddingAPremiseChangesThePreimage(t *testing.T) {
	unconditional := BundleFragment{ItemBody: ItemBody{Content: "body"}}
	conditional := unconditional
	conditional.Premise = "only sometimes"

	a, _ := unconditional.ContentPayload(false)
	b, _ := conditional.ContentPayload(false)
	assert.NotEqual(t, a, b)
}

// EVERY field of a fragment is either PRESENTED — it moves the preimage — or
// carries an explicit `surface:` classification naming why it never reaches
// the agent. A field that is neither fails here, so adding a
// field forces the decision at build time instead of defaulting to "unsigned
// and silently unprotected", which is how the premise arrived unsigned. A
// field that is both — classified as non-presented yet moving the preimage —
// fails too: the classification would be a lie.
//
// This reflects over the struct rather than restating it. There is no field
// list in this test to go stale. The same walk runs over BundleCommand and
// BundleSkill (command_surface_test.go).
func TestEveryFieldIsClassified(t *testing.T) {
	base := BundleFragment{
		ItemBody: fullyPopulatedItemBody(),
		Premise:  "premise",
	}
	assertEveryFieldClassified(t, base, func(v reflect.Value) [][]byte {
		f := v.Interface().(BundleFragment)
		raw, _ := f.ContentPayload(false)
		dist, _ := f.ContentPayload(true)
		return [][]byte{raw, dist}
	})
}

// fullyPopulatedItemBody gives every field a value such that perturbing any
// PRESENTED field is observable in at least one form: Distilled is non-empty
// and NoDistill is false, so the distilled form is actually selected when
// preferred and flipping NoDistill changes what is served.
func fullyPopulatedItemBody() ItemBody {
	return ItemBody{
		Tags:         []string{"tag"},
		Notes:        "notes",
		Installation: "installation",
		Content:      "content",
		ContentHash:  "sha256:recorded",
		Distilled:    "distilled",
		DistilledBy:  "distiller",
		NoDistill:    false,
	}
}

func assertEveryFieldClassified(t *testing.T, base any, preimages func(reflect.Value) [][]byte) {
	t.Helper()
	baseline := preimages(reflect.ValueOf(base))
	walkFields(t, reflect.TypeOf(base), nil, func(path []int, field reflect.StructField) {
		mutated := reflect.New(reflect.TypeOf(base)).Elem()
		mutated.Set(reflect.ValueOf(base))
		perturb(t, mutated.FieldByIndex(path), field.Name)
		moved := !equalPreimages(baseline, preimages(mutated))

		tag, classified := field.Tag.Lookup(surfaceTagKey)
		switch {
		case moved && classified:
			t.Errorf("%s is tagged %s:%q but moves the preimage: it IS presented, so the classification lies",
				field.Name, surfaceTagKey, tag)
		case !moved && !classified:
			t.Errorf("%s neither moves the preimage nor carries a %s tag: decide whether the agent sees it "+
				"(add it to the kind's surface model) or tag it with one of %v", field.Name, surfaceTagKey, surfaceClassifications())
		case !moved && classified:
			assert.Contains(t, surfaceClassifications(), NonPresented(tag),
				"%s carries an unknown %s classification %q", field.Name, surfaceTagKey, tag)
		}
	})
}

// walkFields visits every leaf field, descending into struct-typed fields —
// an inlined ItemBody — so each is
// classified field by field rather than as one blob. A struct that is itself
// tagged is classified as a whole and not entered: the tag is the decision
// for everything under it.
func walkFields(t *testing.T, typ reflect.Type, prefix []int, visit func(path []int, f reflect.StructField)) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		path := append(append([]int{}, prefix...), i)
		if _, tagged := f.Tag.Lookup(surfaceTagKey); !tagged && f.Type.Kind() == reflect.Struct {
			walkFields(t, f.Type, path, visit)
			continue
		}
		visit(path, f)
	}
}

// perturb changes a field's value in a way that is distinguishable from the
// baseline for every kind the item shapes currently use. A new kind fails
// loudly so the test grows with the struct rather than silently skipping.
//
// A nil pointer is the ABSENT value; its perturbation is presence with the
// zero value, so a tri-state field (an opt-out `*bool`, where nil means
// enabled) is perturbed to the state absence does not mean. A map is copied
// before it gains a key: the mutated item is a shallow copy of the base and
// must not reach back into the base's map.
func perturb(t *testing.T, v reflect.Value, name string) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "\x00perturbed")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int:
		v.SetInt(v.Int() + 1)
	case reflect.Slice:
		v.Set(reflect.Append(v, reflect.Zero(v.Type().Elem())))
	case reflect.Pointer:
		perturbPointer(t, v, name)
	case reflect.Map:
		perturbMap(v)
	default:
		require.Failf(t, "unhandled field kind", "%s has kind %s; teach perturb how to change it", name, v.Kind())
	}
}

// perturbPointer sets a nil pointer to the zero value, and otherwise points
// at a perturbed copy of what it pointed at.
func perturbPointer(t *testing.T, v reflect.Value, name string) {
	t.Helper()
	if v.IsNil() {
		v.Set(reflect.New(v.Type().Elem()))
		return
	}
	fresh := reflect.New(v.Type().Elem())
	fresh.Elem().Set(v.Elem())
	perturb(t, fresh.Elem(), name)
	v.Set(fresh)
}

// perturbMap sets a copy of the map with one more key. Per-engine blocks are
// presented through the block the frozen preimage contract canonicalises
// (CommandSurface.ExportsPayload); a block for another engine is outside
// it. Perturb the contract block, which is what "the exports changed" means
// to the preimage.
func perturbMap(v reflect.Value) {
	if v.Type() == reflect.TypeOf(EngineBlocks(nil)) {
		copied := EngineBlocks{}
		for _, k := range v.MapKeys() {
			copied[k.String()] = v.MapIndex(k).Bytes()
		}
		copied[preimageContractEngine] = []byte(`{"enabled":false,"description":"perturbed"}`)
		v.Set(reflect.ValueOf(copied))
		return
	}
	copied := reflect.MakeMap(v.Type())
	for _, k := range v.MapKeys() {
		copied.SetMapIndex(k, v.MapIndex(k))
	}
	copied.SetMapIndex(reflect.ValueOf("perturbed").Convert(v.Type().Key()), reflect.Zero(v.Type().Elem()))
	v.Set(copied)
}

func equalPreimages(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

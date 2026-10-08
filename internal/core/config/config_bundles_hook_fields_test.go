package config

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// bundleHookFieldsNotCarried are the BundleHook fields extractHooksFromBundle
// deliberately does NOT copy onto wire.Hook, each with the reason. Every other
// BundleHook field must land on the wire.Hook field of the same name.
var bundleHookFieldsNotCarried = map[string]string{
	"Tags":  "consumed during extraction: bundles.LinkWithholds reads them to withhold a hook linked to an ungranted MCP server",
	"Order": "consumed during extraction: it sequences the hooks within one event, and the sort's result is the carried order",
}

// populateDistinct sets every field of the struct v points at to a non-zero
// value distinct from every other field's, so a mapping that copies one field
// into another's slot is caught as surely as one that drops it. A field kind
// it does not know fails the test, so a new field type forces this helper to
// be taught about it rather than silently left zero.
func populateDistinct(t *testing.T, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := v.Field(i)
		name := typ.Field(i).Name
		switch f.Kind() {
		case reflect.String:
			f.SetString("value-of-" + name)
		case reflect.Int:
			f.SetInt(int64(1000 + i))
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			if f.Type().Elem().Kind() != reflect.String {
				t.Fatalf("BundleHook.%s: slice of %s — teach populateDistinct this kind", name, f.Type().Elem())
			}
			f.Set(reflect.ValueOf([]string{"value-of-" + name, fmt.Sprint(i)}))
		case reflect.Pointer:
			if f.Type().Elem().Kind() != reflect.Int {
				t.Fatalf("BundleHook.%s: pointer to %s — teach populateDistinct this kind", name, f.Type().Elem())
			}
			n := 2000 + i
			f.Set(reflect.ValueOf(&n))
		default:
			t.Fatalf("BundleHook.%s: kind %s — teach populateDistinct this kind", name, f.Kind())
		}
	}
}

// TestExtractHooksFromBundle_CarriesEveryBundleHookField binds the bundle ->
// wire leg of the hook shape. wire.Hook and bundles.BundleHook must gain a field
// together, and a field added to both but not copied in extractHooksFromBundle
// compiles, round-trips through the tree, and is then silently dropped before
// any engine sees it. This test reflects over BundleHook so it needs no edit
// when a field is added: the new field is populated, and either lands on the
// wire.Hook field of the same name or the test fails — unless it is listed in
// bundleHookFieldsNotCarried with the reason.
//
// It runs every BundleHooks event, so an event left out of the UnifiedHooks
// literal fails here too.
func TestExtractHooksFromBundle_CarriesEveryBundleHookField(t *testing.T) {
	var in bundles.BundleHook
	populateDistinct(t, reflect.ValueOf(&in).Elem())

	hookType := reflect.TypeOf(in)
	for name := range bundleHookFieldsNotCarried {
		if _, ok := hookType.FieldByName(name); !ok {
			t.Errorf("bundleHookFieldsNotCarried lists %q, which BundleHook no longer has: remove it", name)
		}
	}

	eventsType := reflect.TypeOf(bundles.BundleHooks{})
	for e := 0; e < eventsType.NumField(); e++ {
		event := eventsType.Field(e).Name
		t.Run(event, func(t *testing.T) {
			var hooks bundles.BundleHooks
			reflect.ValueOf(&hooks).Elem().Field(e).Set(reflect.ValueOf([]bundles.BundleHook{in}))

			got, withheld := extractHooksFromBundle(report.Reporter{}, readWithHooks(t, hooks), mustLocalRef(t, "src"), bundles.LinksUnchecked())
			if len(withheld) != 0 {
				t.Fatalf("withheld %v with links unchecked", withheld)
			}
			slot := reflect.ValueOf(got).FieldByName(event)
			if !slot.IsValid() {
				t.Fatalf("wire.UnifiedHooks has no %s field for BundleHooks.%s", event, event)
			}
			out, ok := slot.Interface().([]wire.Hook)
			if !ok || len(out) != 1 {
				t.Fatalf("UnifiedHooks.%s = %#v, want exactly the one hook — the event is not carried", event, slot.Interface())
			}
			wv := reflect.ValueOf(out[0])

			for i := 0; i < hookType.NumField(); i++ {
				name := hookType.Field(i).Name
				if _, skip := bundleHookFieldsNotCarried[name]; skip {
					continue
				}
				dst := wv.FieldByName(name)
				if !dst.IsValid() {
					t.Errorf("BundleHook.%s has no wire.Hook field of the same name: add one, or list it in bundleHookFieldsNotCarried with the reason", name)
					continue
				}
				want := reflect.ValueOf(in).Field(i).Interface()
				if !reflect.DeepEqual(dst.Interface(), want) {
					t.Errorf("wire.Hook.%s = %#v, want %#v: extractHooksFromBundle does not carry BundleHook.%s", name, dst.Interface(), want, name)
				}
			}
		})
	}
}

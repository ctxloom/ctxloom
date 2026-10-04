package operations

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
)

// authoredItemBodyFields are the ItemBody fields an edit takes from its input.
// Every OTHER ItemBody field is treated as derived from Content and must be
// cleared when Content changes, at every edit site. A field added to ItemBody
// therefore fails this test until it is either cleared by invalidateDerived or
// listed here as authored — a derived artifact can never silently outlive the
// text it was derived from.
var authoredItemBodyFields = map[string]bool{
	"Tags": true, "Notes": true, "Installation": true, "Content": true, "NoDistill": true,
}

// staleItemBody returns an ItemBody with every field set to a non-zero value,
// so a field an edit forgets to clear is visible as non-zero afterwards.
func staleItemBody(t *testing.T) bundles.ItemBody {
	t.Helper()
	var b bundles.ItemBody
	v := reflect.ValueOf(&b).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.String:
			f.SetString("stale-" + v.Type().Field(i).Name)
		case reflect.Bool:
			f.SetBool(true)
		case reflect.Slice:
			f.Set(reflect.Append(reflect.MakeSlice(f.Type(), 0, 1), reflect.New(f.Type().Elem()).Elem()))
		default:
			t.Fatalf("ItemBody.%s has kind %s; teach staleItemBody to populate it", v.Type().Field(i).Name, f.Kind())
		}
	}
	return b
}

func TestContentEditClearsEveryDerivedField(t *testing.T) {
	type input struct {
		Content, Notes, Installation string
		Tags                         []string
		NoDistill                    bool
	}
	in := input{Content: "new content", Notes: "n", Installation: "i", Tags: []string{"t"}}

	sites := map[string]func(existing bundles.ItemBody) bundles.ItemBody{
		"fragment": func(existing bundles.ItemBody) bundles.ItemBody {
			b := &bundles.Bundle{Fragments: map[string]bundles.BundleFragment{"x": {ItemBody: existing}}}
			applyFragmentEdits(b, map[string]BundleFragmentInput{"x": {
				Content: in.Content, Notes: in.Notes, Installation: in.Installation, Tags: in.Tags, NoDistill: in.NoDistill,
			}}, nil, nil)
			return b.Fragments["x"].ItemBody
		},
		"command": func(existing bundles.ItemBody) bundles.ItemBody {
			b := &bundles.Bundle{Commands: map[string]bundles.BundleCommand{"x": {ItemBody: existing}}}
			applyPromptEdits(b, map[string]BundleCommandInput{"x": {
				Content: in.Content, Notes: in.Notes, Installation: in.Installation, Tags: in.Tags, NoDistill: in.NoDistill,
			}}, nil, nil)
			return b.Commands["x"].ItemBody
		},
	}

	want := reflect.ValueOf(in)
	for site, edit := range sites {
		t.Run(site, func(t *testing.T) {
			got := reflect.ValueOf(edit(staleItemBody(t)))
			for i := range got.NumField() {
				name := got.Type().Field(i).Name
				field := got.Field(i)
				if authoredItemBodyFields[name] {
					require.Equal(t, want.FieldByName(name).Interface(), field.Interface(),
						"authored field %s must take the input's value", name)
					continue
				}
				require.True(t, field.IsZero(), "derived field %s survived a content edit at the %s site", name, site)
			}
		})
	}
}

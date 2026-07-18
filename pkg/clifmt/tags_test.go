package clifmt

import (
	"reflect"
	"testing"
)

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"name":       "Name",
		"first_name": "First Name",
		"FirstName":  "First Name",
		"HTTPServer": "HTTP Server",
		"ID":         "ID",
		"id":         "Id",
		"createdAt":  "Created At",
		"created_at": "Created At",
		"a":          "A",
		"":           "",
		"URLPath":    "URL Path",
		"already Ok": "Already Ok",
		"multi__gap": "Multi Gap",
		"trailing_":  "Trailing",
		"_leading":   "Leading",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

type tagFixture struct {
	Plain     string `json:"plain"`
	Labeled   string `json:"labeled" label:"Custom Label"`
	Skipped   string `json:"-"`
	NoTag     string
	Empty     string `json:"empty,omitempty"`
	ColTagged string `json:"col_tagged" col:"Short"`
}

func TestParseJSONTag(t *testing.T) {
	typ := reflect.TypeOf(tagFixture{})

	f, _ := typ.FieldByName("Plain")
	name, skip, omitempty := parseJSONTag(f.Tag)
	if name != "plain" || skip || omitempty {
		t.Errorf("Plain: got (%q, %v, %v)", name, skip, omitempty)
	}

	f, _ = typ.FieldByName("Skipped")
	_, skip, _ = parseJSONTag(f.Tag)
	if !skip {
		t.Errorf("Skipped: expected skip=true")
	}

	f, _ = typ.FieldByName("NoTag")
	name, skip, _ = parseJSONTag(f.Tag)
	if name != "" || skip {
		t.Errorf("NoTag: got (%q, %v)", name, skip)
	}

	f, _ = typ.FieldByName("Empty")
	name, skip, omitempty = parseJSONTag(f.Tag)
	if name != "empty" || skip || !omitempty {
		t.Errorf("Empty: got (%q, %v, %v)", name, skip, omitempty)
	}
}

func TestResolveLabel(t *testing.T) {
	typ := reflect.TypeOf(tagFixture{})

	f, _ := typ.FieldByName("Plain")
	if got := resolveLabel(f, "plain"); got != "Plain" {
		t.Errorf("Plain label = %q, want %q", got, "Plain")
	}

	f, _ = typ.FieldByName("Labeled")
	if got := resolveLabel(f, "labeled"); got != "Custom Label" {
		t.Errorf("Labeled label = %q, want %q", got, "Custom Label")
	}

	f, _ = typ.FieldByName("NoTag")
	if got := resolveLabel(f, ""); got != "No Tag" {
		t.Errorf("NoTag label = %q, want %q", got, "No Tag")
	}
}

func TestResolveCol(t *testing.T) {
	typ := reflect.TypeOf(tagFixture{})

	f, _ := typ.FieldByName("ColTagged")
	label := resolveLabel(f, "col_tagged")
	if got := resolveCol(f, label); got != "Short" {
		t.Errorf("ColTagged col = %q, want %q", got, "Short")
	}

	f, _ = typ.FieldByName("Plain")
	label = resolveLabel(f, "plain")
	if got := resolveCol(f, label); got != label {
		t.Errorf("Plain col = %q, want fallback to label %q", got, label)
	}
}

func TestIsEmptyValue(t *testing.T) {
	var nilPtr *string
	s := "x"
	cases := []struct {
		name string
		v    interface{}
		want bool
	}{
		{"empty string", "", true},
		{"non-empty string", "x", false},
		{"zero int", 0, true},
		{"nonzero int", 1, false},
		{"false bool", false, true},
		{"true bool", true, false},
		{"nil slice", []string(nil), true},
		{"empty slice", []string{}, true},
		{"nonempty slice", []string{"a"}, false},
		{"nil pointer", nilPtr, true},
		{"non-nil pointer", &s, false},
		{"struct never empty", tagFixture{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isEmptyValue(reflect.ValueOf(tc.v))
			if got != tc.want {
				t.Errorf("isEmptyValue(%v) = %v, want %v", tc.v, got, tc.want)
			}
		})
	}
}

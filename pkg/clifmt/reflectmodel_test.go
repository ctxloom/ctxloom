package clifmt

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

type simpleFixture struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type nestedFixture struct {
	Title   string        `json:"title"`
	Owner   simpleFixture `json:"owner"`
	Skipped string        `json:"-"`
}

type tableRowFixture struct {
	ID   string `json:"id" clifmt:"col=ID"`
	Name string `json:"name"`
}

type withTableFixture struct {
	Summary string            `json:"summary"`
	Items   []tableRowFixture `json:"items"`
}

type withOmitFixture struct {
	Kept    string `json:"kept"`
	Omitted string `json:"omitted,omitempty"`
}

type withPointerFixture struct {
	Name *string `json:"name"`
}

func TestDeriveScalars(t *testing.T) {
	got := derive(t, simpleFixture{Name: "widget", Count: 3})
	want := Doc{Field{Label: "Name", Value: "widget"}, Field{Label: "Count", Value: "3"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derive = %#v, want %#v", got, want)
	}
}

func TestDeriveSkipsJSONDash(t *testing.T) {
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}, Skipped: "hidden"}
	for _, b := range derive(t, v) {
		if f, ok := b.(Field); ok && (f.Label == "Skipped" || f.Value == "hidden") {
			t.Fatalf("json:\"-\" field leaked into the view: %#v", b)
		}
	}
}

func TestDeriveNestedStructIsSection(t *testing.T) {
	got := derive(t, nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}})
	want := Doc{
		Field{Label: "Title", Value: "t"},
		Section{Title: "Owner", Body: Doc{Field{Label: "Name", Value: "n"}, Field{Label: "Count", Value: "1"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derive = %#v, want %#v", got, want)
	}
}

func TestDeriveSliceOfStructIsTable(t *testing.T) {
	v := withTableFixture{
		Summary: "two items",
		Items: []tableRowFixture{
			{ID: "1", Name: "a"},
			{ID: "2", Name: "b"},
		},
	}
	got := derive(t, v)
	want := Doc{
		Field{Label: "Summary", Value: "two items"},
		Table{Title: "Items", Columns: []string{"ID", "Name"}, Rows: [][]string{{"1", "a"}, {"2", "b"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derive = %#v, want %#v", got, want)
	}
}

// tableOf returns the one Table a top-level slice derives to.
func tableOf(t *testing.T, doc Doc) Table {
	t.Helper()
	if len(doc) != 1 {
		t.Fatalf("doc = %#v, want one Table", doc)
	}
	tbl, ok := doc[0].(Table)
	if !ok {
		t.Fatalf("doc[0] = %#v, want a Table", doc[0])
	}
	return tbl
}

func TestDeriveTableFromTopLevelSlice(t *testing.T) {
	tbl := tableOf(t, derive(t, []tableRowFixture{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}))
	if tbl.Title != "" {
		t.Errorf("a top-level table has no title, got %q", tbl.Title)
	}
	if !reflect.DeepEqual(tbl.Columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %v", tbl.Columns)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows = %v", tbl.Rows)
	}
}

func TestDeriveTableEmptySliceStillHasColumns(t *testing.T) {
	tbl := tableOf(t, derive(t, []tableRowFixture{}))
	if !reflect.DeepEqual(tbl.Columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %v, want [ID Name] even for an empty slice", tbl.Columns)
	}
	if len(tbl.Rows) != 0 {
		t.Errorf("rows = %v, want none", tbl.Rows)
	}
}

func TestDeriveOmitemptySkipsZeroValue(t *testing.T) {
	got := derive(t, withOmitFixture{Kept: "k", Omitted: ""})
	if want := (Doc{Field{Label: "Kept", Value: "k"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("derive = %#v, want only Kept", got)
	}
	if got := derive(t, withOmitFixture{Kept: "k", Omitted: "here"}); len(got) != 2 {
		t.Fatalf("expected 2 fields when Omitted is set, got %#v", got)
	}
}

func TestDeriveNilPointerRendersEmpty(t *testing.T) {
	got := derive(t, withPointerFixture{Name: nil})
	if want := (Doc{Field{Label: "Name", Value: ""}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected an empty value for a nil pointer, got %#v", got)
	}
}

func TestDerivePointerToStructDereferenced(t *testing.T) {
	if got := derive(t, &simpleFixture{Name: "widget", Count: 3}); len(got) != 2 {
		t.Fatalf("expected 2 fields from a dereferenced pointer struct, got %#v", got)
	}
}

// ptrStringer's String() is declared on the POINTER receiver, so the VALUE
// type does not satisfy fmt.Stringer while the pointer type does. That split
// is what implementsStringer and typeImplementsStringer used to disagree
// about.
type ptrStringer struct {
	N string `json:"n"`
}

func (p *ptrStringer) String() string { return "PS(" + p.N + ")" }

// TestStringerClassifiersAgreeOnPointerReceiver pins that the value-level and
// type-level "does this have a canonical human string form?" questions get the
// same answer. typeImplementsStringer accepted a pointer receiver and
// implementsStringer did not, so a slice of such a struct was ruled
// "stringable" (and therefore NOT rendered as a table) by the first, then
// failed the second and fell through to Go's default struct dump — rendered
// neither as a table nor via its String().
func TestStringerClassifiersAgreeOnPointerReceiver(t *testing.T) {
	v := reflect.ValueOf(ptrStringer{N: "a"})
	if got, want := implementsStringer(v), typeImplementsStringer(v.Type()); got != want {
		t.Fatalf("implementsStringer=%v but typeImplementsStringer=%v for the same type", got, want)
	}
}

// TestScalarStringUsesPointerReceiverStringer covers the three shapes the
// disagreement leaked into: a top-level slice, a slice-of-pointer field, and a
// plain struct field. All three must show the String() form.
func TestScalarStringUsesPointerReceiverStringer(t *testing.T) {
	type holder struct {
		Ptrs  []*ptrStringer `json:"ptrs"`
		One   ptrStringer    `json:"one"`
		Plain string         `json:"plain"`
	}

	t.Run("top-level slice", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderText(&buf, []ptrStringer{{N: "a"}}); err != nil {
			t.Fatalf("renderText: %v", err)
		}
		if !strings.Contains(buf.String(), "PS(a)") {
			t.Errorf("got %q, want the String() form PS(a)", buf.String())
		}
	})

	t.Run("slice-of-pointer field", func(t *testing.T) {
		var buf bytes.Buffer
		h := holder{Ptrs: []*ptrStringer{{N: "b"}}, Plain: "p"}
		if err := renderText(&buf, h); err != nil {
			t.Fatalf("renderText: %v", err)
		}
		if !strings.Contains(buf.String(), "PS(b)") {
			t.Errorf("got %q, want the String() form PS(b)", buf.String())
		}
	})

	t.Run("struct field", func(t *testing.T) {
		var buf bytes.Buffer
		h := holder{One: ptrStringer{N: "c"}, Plain: "p"}
		if err := renderText(&buf, h); err != nil {
			t.Fatalf("renderText: %v", err)
		}
		if !strings.Contains(buf.String(), "PS(c)") {
			t.Errorf("got %q, want the String() form PS(c)", buf.String())
		}
	})
}

type embeddedInner struct {
	Deep string `json:"deep"`
}

type embeddingRow struct {
	*embeddedInner
	Top string `json:"top"`
}

// TestDeriveTableNilEmbeddedPointerRendersEmptyCell pins the invariant
// tableBlocks' FieldByIndexErr swallow encodes: a nil embedded pointer along a
// promoted field's index path is "nothing to show" for that ONE cell, not an
// error and not a lost column. The column must still appear in the header, the
// cell must be empty, and sibling fields on the same row must be unaffected.
// Without this pin the swallow is indistinguishable from a dropped error.
func TestDeriveTableNilEmbeddedPointerRendersEmptyCell(t *testing.T) {
	rows := []embeddingRow{
		{embeddedInner: &embeddedInner{Deep: "present"}, Top: "one"},
		{embeddedInner: nil, Top: "two"},
	}
	tbl := tableOf(t, derive(t, rows))
	deep := -1
	for i, c := range tbl.Columns {
		if c == "Deep" {
			deep = i
		}
	}
	if deep == -1 {
		t.Fatalf("promoted column Deep missing from %v", tbl.Columns)
	}
	if got := tbl.Rows[0][deep]; got != "present" {
		t.Errorf("row 0 Deep = %q, want %q", got, "present")
	}
	if got := tbl.Rows[1][deep]; got != "" {
		t.Errorf("row 1 Deep = %q, want an empty cell for the nil embedded pointer", got)
	}
	if got := tbl.Rows[1][len(tbl.Columns)-1]; got != "two" {
		t.Errorf("row 1 last column = %q; a nil embedded pointer must not disturb sibling fields", got)
	}
}

// TestDeriveTableNilRowElement characterizes both arms of tableBlocks'
// row-validity guard: a nil element of a slice-of-pointer yields a row of the
// right WIDTH with every cell empty (it is still appended, so row indexes keep
// matching the caller's slice indexes), and a valid element is unaffected.
// The guard depends only on the row, never on the column, so this pins the
// behaviour across moving it out of the per-column loop.
func TestDeriveTableNilRowElement(t *testing.T) {
	rows := []*simpleFixture{nil, {Name: "n", Count: 2}}
	tbl := tableOf(t, derive(t, rows))
	if len(tbl.Rows) != 2 {
		t.Fatalf("got %d rows, want 2 (a nil element must still occupy a row)", len(tbl.Rows))
	}
	if len(tbl.Rows[0]) != len(tbl.Columns) {
		t.Errorf("nil-element row has %d cells, want %d", len(tbl.Rows[0]), len(tbl.Columns))
	}
	for i, c := range tbl.Rows[0] {
		if c != "" {
			t.Errorf("nil-element row cell %d = %q, want empty", i, c)
		}
	}
	if tbl.Rows[1][0] != "n" || tbl.Rows[1][1] != "2" {
		t.Errorf("valid row = %v, want [n 2]", tbl.Rows[1])
	}
}

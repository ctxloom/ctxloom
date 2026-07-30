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
	ID   string `json:"id" col:"ID"`
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

func TestBuildNodeScalars(t *testing.T) {
	v := simpleFixture{Name: "widget", Count: 3}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Scalars) != 2 {
		t.Fatalf("expected 2 scalars, got %d: %+v", len(node.Scalars), node.Scalars)
	}
	if node.Scalars[0].Label != "Name" || node.Scalars[0].Value != "widget" {
		t.Errorf("scalar 0 = %+v", node.Scalars[0])
	}
	if node.Scalars[1].Label != "Count" || node.Scalars[1].Value != "3" {
		t.Errorf("scalar 1 = %+v", node.Scalars[1])
	}
}

func TestBuildNodeSkipsJSONDash(t *testing.T) {
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}, Skipped: "hidden"}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	for _, s := range node.Scalars {
		if s.Label == "Skipped" || s.Value == "hidden" {
			t.Fatalf("json:\"-\" field leaked into scalars: %+v", node.Scalars)
		}
	}
}

func TestBuildNodeNestedStructIsSection(t *testing.T) {
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Sections) != 1 {
		t.Fatalf("expected 1 section, got %d", len(node.Sections))
	}
	sec := node.Sections[0]
	if sec.Label != "Owner" {
		t.Errorf("section label = %q, want Owner", sec.Label)
	}
	if len(sec.Node.Scalars) != 2 || sec.Node.Scalars[0].Value != "n" {
		t.Errorf("section node scalars = %+v", sec.Node.Scalars)
	}
}

func TestBuildNodeSliceOfStructIsTable(t *testing.T) {
	v := withTableFixture{
		Summary: "two items",
		Items: []tableRowFixture{
			{ID: "1", Name: "a"},
			{ID: "2", Name: "b"},
		},
	}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Tables) != 1 {
		t.Fatalf("expected 1 table field, got %d", len(node.Tables))
	}
	tbl := node.Tables[0].Table
	if !reflect.DeepEqual(tbl.Columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %v", tbl.Columns)
	}
	want := [][]string{{"1", "a"}, {"2", "b"}}
	if !reflect.DeepEqual(tbl.Rows, want) {
		t.Errorf("rows = %v, want %v", tbl.Rows, want)
	}
}

func TestBuildTableFromTopLevelSlice(t *testing.T) {
	v := []tableRowFixture{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}
	tbl, err := buildTable(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildTable: %v", err)
	}
	if !reflect.DeepEqual(tbl.Columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %v", tbl.Columns)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("rows = %v", tbl.Rows)
	}
}

func TestBuildTableEmptySliceStillHasColumns(t *testing.T) {
	v := []tableRowFixture{}
	tbl, err := buildTable(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildTable: %v", err)
	}
	if !reflect.DeepEqual(tbl.Columns, []string{"ID", "Name"}) {
		t.Errorf("columns = %v, want [ID Name] even for an empty slice", tbl.Columns)
	}
	if len(tbl.Rows) != 0 {
		t.Errorf("rows = %v, want none", tbl.Rows)
	}
}

func TestBuildNodeOmitemptySkipsZeroValue(t *testing.T) {
	v := withOmitFixture{Kept: "k", Omitted: ""}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Scalars) != 1 || node.Scalars[0].Label != "Kept" {
		t.Fatalf("expected only Kept scalar, got %+v", node.Scalars)
	}

	v2 := withOmitFixture{Kept: "k", Omitted: "here"}
	node2, err := buildNode(reflect.ValueOf(v2))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node2.Scalars) != 2 {
		t.Fatalf("expected 2 scalars when Omitted is set, got %+v", node2.Scalars)
	}
}

func TestBuildNodeNilPointerRendersEmpty(t *testing.T) {
	v := withPointerFixture{Name: nil}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Scalars) != 1 || node.Scalars[0].Value != "" {
		t.Fatalf("expected empty scalar value for nil pointer, got %+v", node.Scalars)
	}
}

func TestBuildNodePointerToStructDereferenced(t *testing.T) {
	v := &simpleFixture{Name: "widget", Count: 3}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Scalars) != 2 {
		t.Fatalf("expected 2 scalars from dereferenced pointer struct, got %+v", node.Scalars)
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

// TestBuildTableNilEmbeddedPointerRendersEmptyCell pins the invariant
// buildTable's FieldByIndexErr swallow encodes: a nil embedded pointer along a
// promoted field's index path is "nothing to show" for that ONE cell, not an
// error and not a lost column. The column must still appear in the header, the
// cell must be empty, and sibling fields on the same row must be unaffected.
// Without this pin the swallow is indistinguishable from a dropped error.
func TestBuildTableNilEmbeddedPointerRendersEmptyCell(t *testing.T) {
	rows := []embeddingRow{
		{embeddedInner: &embeddedInner{Deep: "present"}, Top: "one"},
		{embeddedInner: nil, Top: "two"},
	}
	tbl, err := buildTable(reflect.ValueOf(rows))
	if err != nil {
		t.Fatalf("buildTable: %v", err)
	}
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

// TestBuildTableNilRowElement characterizes both arms of buildTable's
// row-validity guard: a nil element of a slice-of-pointer yields a row of the
// right WIDTH with every cell empty (it is still appended, so row indexes keep
// matching the caller's slice indexes), and a valid element is unaffected.
// The guard depends only on the row, never on the column, so this pins the
// behaviour across moving it out of the per-column loop.
func TestBuildTableNilRowElement(t *testing.T) {
	rows := []*simpleFixture{nil, {Name: "n", Count: 2}}
	tbl, err := buildTable(reflect.ValueOf(rows))
	if err != nil {
		t.Fatalf("buildTable: %v", err)
	}
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

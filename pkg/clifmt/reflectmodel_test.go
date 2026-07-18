package clifmt

import (
	"reflect"
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

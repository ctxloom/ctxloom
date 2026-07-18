package clifmt

import (
	"bytes"
	"testing"
)

func TestRenderTextScalars(t *testing.T) {
	var buf bytes.Buffer
	err := renderText(&buf, simpleFixture{Name: "widget", Count: 3})
	if err != nil {
		t.Fatalf("renderText: %v", err)
	}
	want := "Name: widget\nCount: 3\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderTextNestedSection(t *testing.T) {
	var buf bytes.Buffer
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}}
	if err := renderText(&buf, v); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	want := "Title: t\n\nOwner:\n  Name: n\n  Count: 1\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderTextTopLevelTable(t *testing.T) {
	var buf bytes.Buffer
	v := []tableRowFixture{{ID: "1", Name: "alpha"}, {ID: "2", Name: "b"}}
	if err := renderText(&buf, v); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	want := "ID  NAME\n" +
		"1   alpha\n" +
		"2   b\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderTextFieldTable(t *testing.T) {
	var buf bytes.Buffer
	v := withTableFixture{
		Summary: "two items",
		Items: []tableRowFixture{
			{ID: "1", Name: "a"},
			{ID: "2", Name: "b"},
		},
	}
	if err := renderText(&buf, v); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	want := "Summary: two items\n\nItems:\n" +
		"ID  NAME\n" +
		"1   a\n" +
		"2   b\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderTextTopLevelScalar(t *testing.T) {
	var buf bytes.Buffer
	if err := renderText(&buf, "hello"); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	if buf.String() != "hello\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestRenderTextEmptySliceOfScalars(t *testing.T) {
	var buf bytes.Buffer
	if err := renderText(&buf, []string{"a", "b"}); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	if buf.String() != "a\nb\n" {
		t.Errorf("got %q", buf.String())
	}
}

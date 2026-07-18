package clifmt

import (
	"bytes"
	"testing"
)

func TestRenderMarkdownScalars(t *testing.T) {
	var buf bytes.Buffer
	if err := renderMarkdown(&buf, simpleFixture{Name: "widget", Count: 3}); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "**Name:** widget\n**Count:** 3\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderMarkdownNestedSection(t *testing.T) {
	var buf bytes.Buffer
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}}
	if err := renderMarkdown(&buf, v); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "**Title:** t\n\n## Owner\n\n**Name:** n\n**Count:** 1\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderMarkdownTopLevelTable(t *testing.T) {
	var buf bytes.Buffer
	v := []tableRowFixture{{ID: "1", Name: "alpha"}, {ID: "2", Name: "b"}}
	if err := renderMarkdown(&buf, v); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "| ID | Name |\n" +
		"| --- | --- |\n" +
		"| 1 | alpha |\n" +
		"| 2 | b |\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderMarkdownFieldTable(t *testing.T) {
	var buf bytes.Buffer
	v := withTableFixture{
		Summary: "two items",
		Items: []tableRowFixture{
			{ID: "1", Name: "a"},
		},
	}
	if err := renderMarkdown(&buf, v); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "**Summary:** two items\n\n## Items\n\n" +
		"| ID | Name |\n" +
		"| --- | --- |\n" +
		"| 1 | a |\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderMarkdownEscapesPipes(t *testing.T) {
	var buf bytes.Buffer
	v := []tableRowFixture{{ID: "1", Name: "a|b"}}
	if err := renderMarkdown(&buf, v); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "| ID | Name |\n| --- | --- |\n| 1 | a\\|b |\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestRenderMarkdownTopLevelScalar(t *testing.T) {
	var buf bytes.Buffer
	if err := renderMarkdown(&buf, "hello"); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	if buf.String() != "hello\n" {
		t.Errorf("got %q", buf.String())
	}
}

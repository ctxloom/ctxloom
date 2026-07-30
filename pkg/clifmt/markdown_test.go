package clifmt

import (
	"bytes"
	"strings"
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

// TestMarkdownTableEscapesHeaders pins that a column HEADER goes through the
// same escaping as a cell. writeMarkdownTable escaped every cell but joined
// tbl.Columns raw, so a "|" in a col:/label: tag opened an extra column in the
// header row only — the header and the separator row then disagree on column
// count and GFM stops rendering the block as a table at all. The two rows must
// carry the same number of pipes as the body.
func TestMarkdownTableEscapesHeaders(t *testing.T) {
	type row struct {
		A string `json:"a" col:"pipe|header"`
		B string `json:"b"`
	}
	var buf bytes.Buffer
	if err := renderMarkdown(&buf, []row{{A: "x", B: "y"}}); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header/separator/one row, got %d lines:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], `pipe\|header`) {
		t.Errorf("header row did not escape the pipe: %q", lines[0])
	}
	if got, want := strings.Count(lines[0], "|")-strings.Count(lines[0], `\|`), strings.Count(lines[1], "|"); got != want {
		t.Errorf("header has %d structural pipes, separator has %d:\nheader:    %q\nseparator: %q", got, want, lines[0], lines[1])
	}
}

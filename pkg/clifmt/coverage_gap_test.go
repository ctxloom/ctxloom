package clifmt

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

// --- embedded struct field flattening (mirrors encoding/json promotion) ---

type base struct {
	ID string `json:"id"`
}

type embedFixture struct {
	base
	Name string `json:"name"`
}

func TestDeriveFlattensEmbeddedStruct(t *testing.T) {
	v := embedFixture{base: base{ID: "b1"}, Name: "x"}
	var buf bytes.Buffer
	if err := renderText(&buf, v); err != nil {
		t.Fatalf("renderText: %v", err)
	}
	want := "Id: b1\nName: x\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q (embedded field should be promoted like encoding/json)", buf.String(), want)
	}
}

// --- a slice-of-scalar field is a joined line; a map field is a section ---

type withSliceAndMapFixture struct {
	Tags  []string       `json:"tags"`
	Attrs map[string]int `json:"attrs"`
}

func TestDeriveSliceOfScalarsJoined(t *testing.T) {
	doc := derive(t, withSliceAndMapFixture{Tags: []string{"a", "b", "c"}})
	if len(doc) < 1 || doc[0] != (Field{Label: "Tags", Value: "a, b, c"}) {
		t.Errorf("doc = %#v, want Tags joined as \"a, b, c\"", doc)
	}
}

func TestDeriveMapIsASortedSection(t *testing.T) {
	var got Section
	for _, b := range derive(t, withSliceAndMapFixture{Attrs: map[string]int{"z": 1, "a": 2}}) {
		if s, ok := b.(Section); ok && s.Title == "Attrs" {
			got = s
		}
	}
	want := Doc{Field{Label: "a", Value: "2"}, Field{Label: "z", Value: "1"}}
	if !reflect.DeepEqual(got.Body, want) {
		t.Errorf("Attrs = %#v, want a section of sorted entries %#v", got, want)
	}
}

// --- markdown: top-level slice of scalars renders as a bullet list, and
// nested sections deeper than one level increment heading levels ---

func TestRenderMarkdownTopLevelSliceOfScalarsIsBulletList(t *testing.T) {
	var buf bytes.Buffer
	if err := renderMarkdown(&buf, []string{"a", "b"}); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "- a\n- b\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

type deepFixture struct {
	Mid midFixture `json:"mid"`
}

type midFixture struct {
	Inner simpleFixture `json:"inner"`
}

func TestRenderMarkdownDeeplyNestedSectionsIncrementHeadingLevel(t *testing.T) {
	var buf bytes.Buffer
	v := deepFixture{Mid: midFixture{Inner: simpleFixture{Name: "n", Count: 1}}}
	if err := renderMarkdown(&buf, v); err != nil {
		t.Fatalf("renderMarkdown: %v", err)
	}
	want := "## Mid\n\n### Inner\n\n**Name:** n\n**Count:** 1\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// --- yaml/toml propagate marshal errors for genuinely unsupported types ---

func TestRenderYAMLErrorOnUnsupportedType(t *testing.T) {
	var buf bytes.Buffer
	err := renderYAML(&buf, make(chan int))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestRenderTOMLErrorOnUnsupportedType(t *testing.T) {
	var buf bytes.Buffer
	err := renderTOML(&buf, make(chan int))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
}

// --- the nil-err edge case: a caller passing nil must neither panic nor be handed an empty
// failure report. See TestRenderErrorRejectsNilError in errors_test.go for the
// payload half of that contract. ---

func TestRenderErrorNilErrorArgument(t *testing.T) {
	var buf bytes.Buffer
	err := RenderError(&buf, nil, FormatText)
	if !errors.Is(err, ErrNilError) {
		t.Fatalf("RenderError(nil) = %v, want ErrNilError", err)
	}
	if buf.String() != "" {
		t.Errorf("got %q, want no output", buf.String())
	}
}

var errSentinel = errors.New("sentinel")

func TestRenderErrorWrapsUnderlyingError(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, errSentinel, FormatJSON); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("sentinel")) {
		t.Errorf("expected sentinel message in output, got %q", buf.String())
	}
}

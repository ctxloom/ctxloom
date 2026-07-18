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

func TestBuildNodeFlattensEmbeddedStruct(t *testing.T) {
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

// --- slice-of-scalar and map fields render as a joined scalar line ---

type withSliceAndMapFixture struct {
	Tags  []string       `json:"tags"`
	Attrs map[string]int `json:"attrs"`
}

func TestBuildNodeSliceOfScalarsJoined(t *testing.T) {
	v := withSliceAndMapFixture{Tags: []string{"a", "b", "c"}}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	if len(node.Scalars) < 1 || node.Scalars[0].Value != "a, b, c" {
		t.Errorf("scalars = %+v, want Tags joined as \"a, b, c\"", node.Scalars)
	}
}

func TestBuildNodeMapJoinedSorted(t *testing.T) {
	v := withSliceAndMapFixture{Attrs: map[string]int{"z": 1, "a": 2}}
	node, err := buildNode(reflect.ValueOf(v))
	if err != nil {
		t.Fatalf("buildNode: %v", err)
	}
	var got string
	for _, s := range node.Scalars {
		if s.Label == "Attrs" {
			got = s.Value
		}
	}
	if got != "a=2, z=1" {
		t.Errorf("Attrs joined = %q, want sorted \"a=2, z=1\"", got)
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

// --- Render propagates a struct-classification error for a bad buildTable
// call reached via a slice of a non-struct, non-scalar-friendly type is not
// reachable through the public surface, so this instead checks RenderError
// still returns a well-formed error for a nil err argument (edge case: a
// caller passing nil shouldn't panic). ---

func TestRenderErrorNilErrorArgument(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, nil, FormatText); err != nil {
		t.Fatalf("RenderError(nil): %v", err)
	}
	if buf.String() != "Error: \n" {
		t.Errorf("got %q", buf.String())
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

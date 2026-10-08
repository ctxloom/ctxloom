package clifmt

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"testing"
)

func both(t *testing.T, v any, wantText, wantMD string, opts ...Option) {
	t.Helper()
	for f, want := range map[Format]string{FormatText: wantText, FormatMarkdown: wantMD} {
		var buf bytes.Buffer
		if err := Render(&buf, v, f, opts...); err != nil {
			t.Fatalf("Render(%s): %v", f, err)
		}
		if buf.String() != want {
			t.Errorf("%s:\n got %q\nwant %q", f, buf.String(), want)
		}
	}
}

func TestMap(t *testing.T) {
	m := NewMap().Set("b", 1).Set("a", 2).Set("b", 3)
	if got := m.Keys(); !reflect.DeepEqual(got, []string{"b", "a"}) {
		t.Errorf("Keys = %v, want insertion order with a re-set key kept in place", got)
	}
	if v, ok := m.Get("b"); !ok || v != 3 {
		t.Errorf("Get(b) = %v, %v", v, ok)
	}
	if _, ok := m.Get("zz"); ok {
		t.Error("Get(zz) found a key never set")
	}
	if m.Len() != 2 {
		t.Errorf("Len = %d", m.Len())
	}
	m.Keys()[0] = "mutated"
	if m.Keys()[0] != "b" {
		t.Error("Keys exposes the Map's own slice")
	}
	var zero Map
	zero.Set("k", 1)
	if zero.Len() != 1 {
		t.Error("the zero Map is not usable")
	}
}

// A Map is a JSON object in insertion order, and a nil slice in a value is
// still `[]`, never `null`.
func TestMapJSON(t *testing.T) {
	var nilSlice []string
	m := NewMap().Set("z", 1).Set("a", nilSlice).Set("m", NewMap().Set("y", true).Set("x", nil))
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"z":1,"a":[],"m":{"y":true,"x":null}}`; string(b) != want {
		t.Errorf("json = %s, want %s", b, want)
	}
	var buf bytes.Buffer
	if err := Render(&buf, struct {
		M Map `json:"m"`
	}{M: *m}, FormatJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"z": 1,`) || strings.Index(buf.String(), `"z"`) > strings.Index(buf.String(), `"a"`) {
		t.Errorf("a Map held by value lost its order or encoding: %s", buf.String())
	}
}

type mapFieldFixture struct {
	Name  string         `json:"name"`
	Attrs map[string]int `json:"attrs"`
}

// A map field is a section: one line per entry, keys sorted and verbatim
// (data, never humanized).
func TestRender_MapFieldIsASection(t *testing.T) {
	both(t, mapFieldFixture{Name: "x", Attrs: map[string]int{"z": 1, "a_b": 2}},
		"Name: x\n\nAttrs:\n  a_b: 2\n  z: 1\n",
		"**Name:** x\n\n## Attrs\n\n**a_b:** 2\n**z:** 1\n")
}

// A *Map field keeps insertion order.
func TestRender_OrderedMapFieldKeepsInsertionOrder(t *testing.T) {
	v := struct {
		Cols *Map `json:"cols"`
	}{Cols: NewMap().Set("zeta", "1").Set("alpha", "2")}
	both(t, v, "Cols:\n  zeta: 1\n  alpha: 2\n", "## Cols\n\n**zeta:** 1\n**alpha:** 2\n")
}

func TestRender_TopLevelMapIsLikeAStruct(t *testing.T) {
	both(t, map[string]any{"b": 1, "a": "x"}, "a: x\nb: 1\n", "**a:** x\n**b:** 1\n")
	both(t, NewMap().Set("b", 1).Set("a", "x"), "b: 1\na: x\n", "**b:** 1\n**a:** x\n")
	both(t, map[string]int{}, "(none)\n", "(none)\n")
}

type bundleFrag struct {
	Path string `json:"path"`
	Size int    `json:"size"`
}

// A map whose values are structs renders one sub-section per key, not Go's
// struct syntax.
func TestRender_MapOfStructsIsSubSections(t *testing.T) {
	v := struct {
		Fragments map[string]bundleFrag `json:"fragments"`
	}{Fragments: map[string]bundleFrag{"b": {Path: "p/b", Size: 2}, "a": {Path: "p/a", Size: 1}}}
	both(t, v,
		"Fragments:\n  a:\n    Path: p/a\n    Size: 1\n\n  b:\n    Path: p/b\n    Size: 2\n",
		"## Fragments\n\n### a\n\n**Path:** p/a\n**Size:** 1\n\n### b\n\n**Path:** p/b\n**Size:** 2\n")
}

// Entries are classified like struct fields: scalars, then sections, then
// tables; a nil value is an empty scalar.
func TestRender_MapEntriesAreClassifiedLikeFields(t *testing.T) {
	v := map[string]any{
		"rows":  []bundleFrag{{Path: "r", Size: 1}},
		"inner": map[string]int{"k": 1},
		"plain": "p",
		"none":  nil,
	}
	both(t, v,
		"none: \nplain: p\n\ninner:\n  k: 1\n\nrows:\nPATH  SIZE\nr     1\n",
		"**none:** \n**plain:** p\n\n## inner\n\n**k:** 1\n\n## rows\n\n| Path | Size |\n| --- | --- |\n| r | 1 |\n")
}

// A list of maps with one key set and scalar values is a table.
func TestRender_UniformListOfMapsIsATable(t *testing.T) {
	both(t, []map[string]any{{"b": 1, "a": "x"}, {"a": "y", "b": 2}},
		"A  B\nx  1\ny  2\n",
		"| a | b |\n| --- | --- |\n| x | 1 |\n| y | 2 |\n")
	both(t, []*Map{NewMap().Set("z", 1).Set("a", 2), NewMap().Set("a", 3).Set("z", 4)},
		"Z  A\n1  2\n4  3\n",
		"| z | a |\n| --- | --- |\n| 1 | 2 |\n| 4 | 3 |\n")
	v := struct {
		Rows []any `json:"rows"`
	}{Rows: []any{map[string]any{"k": "v"}}}
	both(t, v, "Rows:\nK\nv\n", "## Rows\n\n| k |\n| --- |\n| v |\n")
}

// A list of maps that differ in keys, or hold nested values, is a run of
// numbered sections.
func TestRender_NonUniformListOfMapsIsSections(t *testing.T) {
	both(t, []map[string]any{{"a": 1}, {"b": 2}},
		"[1]:\n  a: 1\n\n[2]:\n  b: 2\n",
		"## [1]\n\n**a:** 1\n\n## [2]\n\n**b:** 2\n")
	both(t, []map[string]any{{"a": 1, "b": 2}, {"a": 3}},
		"[1]:\n  a: 1\n  b: 2\n\n[2]:\n  a: 3\n",
		"## [1]\n\n**a:** 1\n**b:** 2\n\n## [2]\n\n**a:** 3\n")
	v := struct {
		Rows []map[string]any `json:"rows"`
	}{Rows: []map[string]any{{"a": map[string]int{"n": 1}}}}
	both(t, v, "Rows:\n  [1]:\n    a:\n      n: 1\n", "## Rows\n\n### [1]\n\n#### a\n\n**n:** 1\n")
}

type cellRow struct {
	ID    string            `json:"id"`
	Env   map[string]string `json:"env"`
	Owner bundleFrag        `json:"owner"`
	Tags  []string          `json:"tags"`
	Deep  map[string]any    `json:"deep"`
}

// A nested struct, map or list in a table cell is a compact k=v line in
// json names and map keys, never Go syntax.
func TestRender_NestedValueInATableCellIsInline(t *testing.T) {
	rows := []cellRow{{
		ID:    "1",
		Env:   map[string]string{"B": "2", "A": "1"},
		Owner: bundleFrag{Path: "p", Size: 3},
		Tags:  []string{"x", "y"},
		Deep:  map[string]any{"m": map[string]int{"k": 1}, "l": []int{1, 2}},
	}}
	both(t, rows,
		"ID  ENV       OWNER           TAGS  DEEP\n1   A=1, B=2  path=p, size=3  x, y  l=[1, 2], m={k=1}\n",
		"| Id | Env | Owner | Tags | Deep |\n| --- | --- | --- | --- | --- |\n| 1 | A=1, B=2 | path=p, size=3 | x, y | l=[1, 2], m={k=1} |\n")
}

type verdict string

type textKey struct{ a, b string }

func (k textKey) MarshalText() ([]byte, error) { return []byte(k.a + "/" + k.b), nil }

// Map keys are stringified and sorted the way encoding/json does it.
func TestRender_MapKeysFollowEncodingJSON(t *testing.T) {
	both(t, map[int]string{9: "nine", 10: "ten"}, "10: ten\n9: nine\n", "**10:** ten\n**9:** nine\n")
	both(t, map[verdict]int{"keep": 1, "drop": 2}, "drop: 2\nkeep: 1\n", "**drop:** 2\n**keep:** 1\n")
	both(t, map[textKey]int{{"b", "1"}: 1, {"a", "2"}: 2}, "a/2: 2\nb/1: 1\n", "**a/2:** 2\n**b/1:** 1\n")
}

// A field typed any follows its dynamic value.
func TestRender_AnyFieldFollowsItsDynamicValue(t *testing.T) {
	v := struct {
		Data any `json:"data"`
	}{Data: map[string]any{"k": "v"}}
	both(t, v, "Data:\n  k: v\n", "## Data\n\n**k:** v\n")
}

// A map entry is addressed by its key as a path segment, and any key passes
// validation (keys are data, unknown until run time).
func TestAtAddressesMapEntries(t *testing.T) {
	v := mapFieldFixture{Name: "x", Attrs: map[string]int{"a": 1, "b": 2}}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatText, At("attrs.b", func(*ViewCtx, any) (Doc, error) { return Doc{Para("two")}, nil })); err != nil {
		t.Fatal(err)
	}
	if want := "Name: x\n\nAttrs:\n  a: 1\n  b: two\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	if err := Render(io.Discard, v, FormatText, At("attrs.anything", func(*ViewCtx, any) (Doc, error) { return nil, nil })); err != nil {
		t.Errorf("a map key path was rejected: %v", err)
	}
	if err := Render(io.Discard, v, FormatText, At("attrs.k.deeper", func(*ViewCtx, any) (Doc, error) { return nil, nil })); err == nil {
		t.Error("a path below an int map value was accepted")
	}
	ordered := struct {
		M *Map `json:"m"`
	}{M: NewMap().Set("k", 1)}
	if err := Render(io.Discard, ordered, FormatText, At("m.k.anything", func(*ViewCtx, any) (Doc, error) { return nil, nil })); err != nil {
		t.Errorf("a path into a *Map (values typed any) was rejected: %v", err)
	}
}

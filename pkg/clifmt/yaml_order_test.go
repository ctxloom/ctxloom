package clifmt

import (
	"bytes"
	"strings"
	"testing"
)

type orderMid struct {
	Y int `json:"q"`
	X int `json:"p"`
}

type orderFixture struct {
	Zeta  string         `json:"zeta"`
	Alpha int            `json:"alpha"`
	Mid   orderMid       `json:"mid"`
	M     *Map           `json:"m"`
	Plain map[string]int `json:"plain"`
	Rows  []orderMid     `json:"rows"`
}

func orderValue() orderFixture {
	return orderFixture{
		Zeta:  "z",
		Alpha: 1,
		Mid:   orderMid{Y: 1, X: 2},
		M:     NewMap().Set("b", 1).Set("a", 2),
		Plain: map[string]int{"b": 2, "a": 1},
		Rows:  []orderMid{{Y: 3, X: 4}},
	}
}

// yaml follows the json contract's key order: struct field order, a Map's
// insertion order, and (as json writes it) a Go map's sorted keys.
func TestRenderYAMLFollowsJSONKeyOrder(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, orderValue(), FormatYAML); err != nil {
		t.Fatal(err)
	}
	want := "zeta: z\n" +
		"alpha: 1\n" +
		"mid:\n" +
		"  q: 1\n" +
		"  p: 2\n" +
		"m:\n" +
		"  b: 1\n" +
		"  a: 2\n" +
		"plain:\n" +
		"  a: 1\n" +
		"  b: 2\n" +
		"rows:\n" +
		"  - q: 3\n" +
		"    p: 4\n"
	if buf.String() != want {
		t.Errorf("yaml:\n%s\nwant:\n%s", buf.String(), want)
	}

	buf.Reset()
	if err := Render(&buf, NewMap().Set("z", []string(nil)).Set("a", map[string]any{}), FormatYAML); err != nil {
		t.Fatal(err)
	}
	if want := "z: []\na: {}\n"; buf.String() != want {
		t.Errorf("top-level Map yaml = %q, want %q", buf.String(), want)
	}
}

// TOML stays key-sorted: go-toml/v2 sorts map keys and has no ordered
// generic form.
func TestRenderTOMLStaysKeySorted(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, orderValue(), FormatTOML); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Errorf("toml keys not sorted:\n%s", out)
	}
	m := out[strings.Index(out, "[m]"):]
	if strings.Index(m, "a =") > strings.Index(m, "b =") {
		t.Errorf("toml [m] keys not sorted:\n%s", out)
	}
}

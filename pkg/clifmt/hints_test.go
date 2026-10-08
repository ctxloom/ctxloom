package clifmt

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseHint(t *testing.T) {
	cases := []struct {
		raw     string
		want    hint
		wantErr string
	}{
		{raw: "", want: hint{}},
		{raw: "-", want: hint{hide: true}},
		{raw: "label=Owning Team", want: hint{label: "Owning Team"}},
		{raw: "col=HARP", want: hint{col: "HARP"}},
		{raw: "label=Harp,col=HARP,role=id", want: hint{label: "Harp", col: "HARP", role: "id"}},
		{raw: "role=primary", want: hint{role: "primary"}},
		{raw: "role=status", want: hint{role: "status"}},
		{raw: "role=detail", want: hint{role: "detail"}},
		{raw: "col=pipe|header", want: hint{col: "pipe|header"}},
		{raw: "role=headline", wantErr: `unknown role "headline"`},
		{raw: "colour=red", wantErr: `unknown key "colour"`},
		{raw: "label=a=b", wantErr: `"label=a=b" is not key=value`},
		{raw: "label", wantErr: `"label" is not key=value`},
		{raw: "label=", wantErr: `key "label" has an empty value`},
		{raw: "label=a,,col=b", wantErr: `"" is not key=value`},
		{raw: "label=a,label=b", wantErr: `key "label" given twice`},
		{raw: "-,label=a", wantErr: `"-" must stand alone`},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseHint(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseHint(%q) = %+v, want error containing %q", tc.raw, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseHint(%q) error = %q, want it to contain %q", tc.raw, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHint(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("parseHint(%q) = %+v, want %+v", tc.raw, got, tc.want)
			}
		})
	}
}

type bareTagged struct {
	Name string `json:"name" label:"Custom Label" col:"CUSTOM"`
}

// A bare label:/col: tag is not part of clifmt's grammar: the reflective
// views ignore it and derive the label from the json name. (The test-arch
// gate keeps production code from carrying one by mistake.)
func TestRender_BareLabelColTagsAreNotRead(t *testing.T) {
	var node bytes.Buffer
	if err := Render(&node, bareTagged{Name: "x"}, FormatText); err != nil {
		t.Fatal(err)
	}
	if got, want := node.String(), "Name: x\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	var tbl bytes.Buffer
	if err := Render(&tbl, []bareTagged{{Name: "x"}}, FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tbl.String(), "| Name |") {
		t.Errorf("markdown table header ignores the bare col tag; got %q", tbl.String())
	}
}

type namespacedTagged struct {
	Name string `json:"name" clifmt:"label=Custom Label,col=CUSTOM"`
}

func TestRender_ClifmtTagSetsLabelAndCol(t *testing.T) {
	var node bytes.Buffer
	if err := Render(&node, namespacedTagged{Name: "x"}, FormatText); err != nil {
		t.Fatal(err)
	}
	if got, want := node.String(), "Custom Label: x\n"; got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	var tbl bytes.Buffer
	if err := Render(&tbl, []namespacedTagged{{Name: "x"}}, FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tbl.String(), "| CUSTOM |") {
		t.Errorf("markdown table header = %q, want the col hint", tbl.String())
	}
}

type labelOnlyTagged struct {
	Name string `json:"name" clifmt:"label=Custom Label"`
}

// A field with a label hint and no col hint heads its table column with the
// label.
func TestRender_ColFallsBackToLabelHint(t *testing.T) {
	var tbl bytes.Buffer
	if err := Render(&tbl, []labelOnlyTagged{{Name: "x"}}, FormatMarkdown); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tbl.String(), "| Custom Label |") {
		t.Errorf("markdown table header = %q, want the label hint", tbl.String())
	}
}

type hiddenTagged struct {
	Shown  string `json:"shown"`
	Hidden string `json:"hidden" clifmt:"-"`
}

// clifmt:"-" hides a field from the derived human views only; the json
// contract keeps it.
func TestRender_HiddenFieldStaysInStructuredOutput(t *testing.T) {
	v := hiddenTagged{Shown: "a", Hidden: "b"}
	for _, f := range []Format{FormatText, FormatMarkdown} {
		var buf bytes.Buffer
		if err := Render(&buf, v, f); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(buf.String(), "b") {
			t.Errorf("%s shows the hidden field: %q", f, buf.String())
		}
		buf.Reset()
		if err := Render(&buf, []hiddenTagged{v}, f); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(buf.String()), "hidden") {
			t.Errorf("%s table has a column for the hidden field: %q", f, buf.String())
		}
	}
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML} {
		var buf bytes.Buffer
		if err := Render(&buf, v, f); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(buf.String(), "hidden") {
			t.Errorf("%s dropped the hidden field: %q", f, buf.String())
		}
	}
}

type malformedTagged struct {
	OK  string `json:"ok"`
	Bad string `json:"bad" clifmt:"colour=red"`
}

// A malformed clifmt tag is a programming error and fails the render loudly,
// naming the type and the field, wherever the type appears.
func TestRender_MalformedClifmtTagFails(t *testing.T) {
	for _, v := range []any{malformedTagged{}, []malformedTagged{{}}, struct {
		Inner malformedTagged `json:"inner"`
	}{}} {
		for _, f := range []Format{FormatText, FormatMarkdown} {
			var buf bytes.Buffer
			err := Render(&buf, v, f)
			if err == nil {
				t.Fatalf("Render(%T, %s) accepted a malformed clifmt tag", v, f)
			}
			for _, want := range []string{"malformedTagged", "Bad", `unknown key "colour"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Render(%T, %s) error %q does not name %q", v, f, err, want)
				}
			}
		}
	}
}

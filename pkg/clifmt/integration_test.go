package clifmt

import (
	"bytes"
	"testing"
)

// reportResult is a representative CLI Result: scalars, a nested section,
// and a slice-of-struct table together, the shape a real command's output
// struct is expected to take.
type reportResult struct {
	Name  string     `json:"name"`
	Count int        `json:"count"`
	Owner ownerInfo  `json:"owner"`
	Rows  []rowEntry `json:"rows"`
}

type ownerInfo struct {
	Team string `json:"team" label:"Owning Team"`
}

type rowEntry struct {
	ID     string `json:"id" col:"ID"`
	Status string `json:"status"`
}

func exampleReport() reportResult {
	return reportResult{
		Name:  "nightly-build",
		Count: 2,
		Owner: ownerInfo{Team: "platform"},
		Rows: []rowEntry{
			{ID: "r1", Status: "ok"},
			{ID: "r2", Status: "failed"},
		},
	}
}

func TestRenderAllFormatsTableDriven(t *testing.T) {
	cases := []struct {
		format Format
		want   string
	}{
		{
			format: FormatJSON,
			want: "{\n" +
				"  \"name\": \"nightly-build\",\n" +
				"  \"count\": 2,\n" +
				"  \"owner\": {\n" +
				"    \"team\": \"platform\"\n" +
				"  },\n" +
				"  \"rows\": [\n" +
				"    {\n" +
				"      \"id\": \"r1\",\n" +
				"      \"status\": \"ok\"\n" +
				"    },\n" +
				"    {\n" +
				"      \"id\": \"r2\",\n" +
				"      \"status\": \"failed\"\n" +
				"    }\n" +
				"  ]\n" +
				"}\n",
		},
		{
			format: FormatText,
			want: "Name: nightly-build\n" +
				"Count: 2\n" +
				"\n" +
				"Owner:\n" +
				"  Owning Team: platform\n" +
				"\n" +
				"Rows:\n" +
				"ID  STATUS\n" +
				"r1  ok\n" +
				"r2  failed\n",
		},
		{
			format: FormatMarkdown,
			want: "**Name:** nightly-build\n" +
				"**Count:** 2\n" +
				"\n" +
				"## Owner\n" +
				"\n" +
				"**Owning Team:** platform\n" +
				"\n" +
				"## Rows\n" +
				"\n" +
				"| ID | Status |\n" +
				"| --- | --- |\n" +
				"| r1 | ok |\n" +
				"| r2 | failed |\n",
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.format), func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(&buf, exampleReport(), tc.format); err != nil {
				t.Fatalf("Render(%s): %v", tc.format, err)
			}
			if buf.String() != tc.want {
				t.Errorf("Render(%s) got:\n%s\nwant:\n%s", tc.format, buf.String(), tc.want)
			}
		})
	}

	// YAML and TOML assert structural properties rather than a byte-exact
	// string, since map key ordering in the generic round-trip is alphabetic
	// (yaml.v3/go-toml/v2's own determinism) rather than struct field order.
	t.Run("yaml_field_identity", func(t *testing.T) {
		var buf bytes.Buffer
		if err := Render(&buf, exampleReport(), FormatYAML); err != nil {
			t.Fatalf("Render(yaml): %v", err)
		}
		want := "count: 2\n" +
			"name: nightly-build\n" +
			"owner:\n" +
			"    team: platform\n" +
			"rows:\n" +
			"    - id: r1\n" +
			"      status: ok\n" +
			"    - id: r2\n" +
			"      status: failed\n"
		if buf.String() != want {
			t.Errorf("Render(yaml) got:\n%s\nwant:\n%s", buf.String(), want)
		}
	})

	t.Run("toml_field_identity", func(t *testing.T) {
		var buf bytes.Buffer
		if err := Render(&buf, exampleReport(), FormatTOML); err != nil {
			t.Fatalf("Render(toml): %v", err)
		}
		want := "count = 2\n" +
			"name = 'nightly-build'\n" +
			"\n" +
			"[owner]\n" +
			"team = 'platform'\n" +
			"\n" +
			"[[rows]]\n" +
			"id = 'r1'\n" +
			"status = 'ok'\n" +
			"\n" +
			"[[rows]]\n" +
			"id = 'r2'\n" +
			"status = 'failed'\n"
		if buf.String() != want {
			t.Errorf("Render(toml) got:\n%s\nwant:\n%s", buf.String(), want)
		}
	})
}

package clifmt

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderJSONRespectsTagsAndIndent(t *testing.T) {
	var buf bytes.Buffer
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}, Skipped: "hidden"}
	if err := renderJSON(&buf, v); err != nil {
		t.Fatalf("renderJSON: %v", err)
	}
	want := "{\n  \"title\": \"t\",\n  \"owner\": {\n    \"name\": \"n\",\n    \"count\": 1\n  }\n}\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
	if strings.Contains(buf.String(), "hidden") {
		t.Errorf("json:\"-\" field leaked into JSON output: %s", buf.String())
	}
}

func TestRenderYAMLUsesJSONFieldIdentity(t *testing.T) {
	var buf bytes.Buffer
	v := nestedFixture{Title: "t", Owner: simpleFixture{Name: "n", Count: 1}, Skipped: "hidden"}
	if err := renderYAML(&buf, v); err != nil {
		t.Fatalf("renderYAML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "title: t") {
		t.Errorf("expected yaml key from json tag \"title\", got:\n%s", out)
	}
	// yaml.v3 quotes "n" since bare n/y are YAML 1.1 boolean literals; that's
	// correct disambiguation, not a bug, so the assertion allows either form.
	if !strings.Contains(out, "owner:") || !strings.Contains(out, "count: 1") {
		t.Errorf("expected nested owner keys from json tags, got:\n%s", out)
	}
	if !strings.Contains(out, `name: "n"`) && !strings.Contains(out, "name: n\n") {
		t.Errorf("expected owner.name from json tag, got:\n%s", out)
	}
	if strings.Contains(out, "hidden") || strings.Contains(out, "skipped") {
		t.Errorf("json:\"-\" field leaked into YAML output: %s", out)
	}
}

func TestRenderYAMLPreservesIntegers(t *testing.T) {
	var buf bytes.Buffer
	if err := renderYAML(&buf, simpleFixture{Name: "x", Count: 42}); err != nil {
		t.Fatalf("renderYAML: %v", err)
	}
	if !strings.Contains(buf.String(), "count: 42\n") {
		t.Errorf("expected unquoted integer 42, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), `"42"`) || strings.Contains(buf.String(), "'42'") {
		t.Errorf("integer incorrectly quoted: %s", buf.String())
	}
}

func TestRenderTOMLStructAsDocumentRoot(t *testing.T) {
	var buf bytes.Buffer
	if err := renderTOML(&buf, simpleFixture{Name: "x", Count: 42}); err != nil {
		t.Fatalf("renderTOML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "name = 'x'") {
		t.Errorf("expected toml key from json tag \"name\", got:\n%s", out)
	}
	if !strings.Contains(out, "count = 42") {
		t.Errorf("expected toml integer count, got:\n%s", out)
	}
}

func TestRenderTOMLWrapsTopLevelSlice(t *testing.T) {
	var buf bytes.Buffer
	v := []tableRowFixture{{ID: "1", Name: "a"}, {ID: "2", Name: "b"}}
	if err := renderTOML(&buf, v); err != nil {
		t.Fatalf("renderTOML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "[[items]]") {
		t.Errorf("expected a wrapped array-of-tables under [[items]], got:\n%s", out)
	}
	if !strings.Contains(out, "id = '1'") || !strings.Contains(out, "id = '2'") {
		t.Errorf("expected row values in toml output, got:\n%s", out)
	}
}

func TestRenderJSONErrorOnUnmarshalable(t *testing.T) {
	var buf bytes.Buffer
	err := renderJSON(&buf, make(chan int))
	if err == nil {
		t.Fatal("expected an error marshaling an unsupported type, got nil")
	}
}

package yamlx

import (
	"strings"
	"testing"
)

type target struct {
	Name  string   `yaml:"name"`
	Items []string `yaml:"items"`
}

func TestDecodeStrict_ReadsAKnownDocument(t *testing.T) {
	var got target
	if err := DecodeStrict([]byte("name: a\nitems: [x, y]\n"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Name != "a" || len(got.Items) != 2 {
		t.Errorf("decoded %+v, want name=a with 2 items", got)
	}
}

// The whole point of the helper: a key the struct does not model is a typo,
// not something to drop in silence.
func TestDecodeStrict_RejectsAnUnknownField(t *testing.T) {
	var got target
	err := DecodeStrict([]byte("name: a\nnaem: b\n"), &got)
	if err == nil {
		t.Fatal("an unknown field must be rejected, not ignored")
	}
	if !strings.Contains(err.Error(), "naem") {
		t.Errorf("the error must name the offending key; got: %v", err)
	}
}

// An empty document is not an error. Both call sites rely on this: a
// comments-only or zero-byte file decodes to the zero value, and each applies
// its OWN emptiness rule afterwards rather than having one imposed here.
func TestDecodeStrict_TreatsAnEmptyDocumentAsTheZeroValue(t *testing.T) {
	var got target
	if err := DecodeStrict(nil, &got); err != nil {
		t.Fatalf("empty input must decode cleanly: %v", err)
	}
	if got.Name != "" {
		t.Errorf("expected the zero value, got %+v", got)
	}
	if err := DecodeStrict([]byte("# just a comment\n"), &got); err != nil {
		t.Fatalf("comments-only input must decode cleanly: %v", err)
	}
}

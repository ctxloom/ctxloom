package clifmt

import (
	"io"
	"testing"
)

// renderText and renderMarkdown render v in one human format with no
// options: the shorthand the per-format tests use.
func renderText(w io.Writer, v any) error     { return Render(w, v, FormatText) }
func renderMarkdown(w io.Writer, v any) error { return Render(w, v, FormatMarkdown) }

// derive is v's derived Doc with no options.
func derive(t *testing.T, v any) Doc {
	t.Helper()
	d := &deriver{format: FormatText}
	doc, err := d.root(v)
	if err != nil {
		t.Fatalf("derive(%T): %v", v, err)
	}
	return doc
}

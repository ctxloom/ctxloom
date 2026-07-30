package clifmt

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestRenderErrorStructuredFormats(t *testing.T) {
	boom := errors.New("something broke")
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML} {
		var buf bytes.Buffer
		if err := RenderError(&buf, boom, f); err != nil {
			t.Fatalf("RenderError(%s): %v", f, err)
		}
		if !strings.Contains(buf.String(), "something broke") {
			t.Errorf("RenderError(%s) output missing message:\n%s", f, buf.String())
		}
	}
}

func TestRenderErrorJSONShape(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, errors.New("boom"), FormatJSON); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	want := "{\n  \"error\": \"boom\"\n}\n"
	if buf.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRenderErrorHumanLineForTextAndMarkdown(t *testing.T) {
	var textBuf, mdBuf bytes.Buffer
	boom := errors.New("disk full")

	if err := RenderError(&textBuf, boom, FormatText); err != nil {
		t.Fatalf("RenderError text: %v", err)
	}
	if textBuf.String() != "Error: disk full\n" {
		t.Errorf("text got %q, want %q", textBuf.String(), "Error: disk full\n")
	}

	if err := RenderError(&mdBuf, boom, FormatMarkdown); err != nil {
		t.Fatalf("RenderError markdown: %v", err)
	}
	if mdBuf.String() != "**Error:** disk full\n" {
		t.Errorf("markdown got %q, want %q", mdBuf.String(), "**Error:** disk full\n")
	}
}

func TestRenderErrorUnsupportedFormat(t *testing.T) {
	var buf bytes.Buffer
	err := RenderError(&buf, errors.New("boom"), Format("bogus"))
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("RenderError error = %v, want wrapping ErrUnsupportedFormat", err)
	}
}

// TestRenderErrorRejectsNilError pins that a nil error is REJECTED rather than
// rendered. A nil error is not a failure to report: rendering it produced a
// well-formed failure envelope carrying an empty message ({"error": ""} /
// "Error: "), which tells a machine consumer that something went wrong while
// naming nothing that did. The payload assertion is the load-bearing half —
// the writer must receive zero bytes, not merely a non-nil return.
func TestRenderErrorRejectsNilError(t *testing.T) {
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML, FormatText, FormatMarkdown} {
		var buf bytes.Buffer
		err := RenderError(&buf, nil, f)
		if err == nil {
			t.Errorf("RenderError(%s, nil) returned no error", f)
		}
		if buf.Len() != 0 {
			t.Errorf("RenderError(%s, nil) wrote %d bytes (%q); a rejected call must write nothing", f, buf.Len(), buf.String())
		}
	}
}

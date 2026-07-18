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

package clifmt

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestRenderDispatchesByFormat(t *testing.T) {
	v := simpleFixture{Name: "x", Count: 1}
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML, FormatText, FormatMarkdown} {
		var buf bytes.Buffer
		if err := Render(&buf, v, f); err != nil {
			t.Fatalf("Render(%s): %v", f, err)
		}
		if buf.Len() == 0 {
			t.Errorf("Render(%s) wrote nothing", f)
		}
	}
}

func TestRenderUnsupportedFormat(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, simpleFixture{}, Format("bogus"))
	if err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("error = %v, want wrapping ErrUnsupportedFormat", err)
	}
}

// escapeHatchFixture is rendered with a WithWriter for markdown only; every
// other format must still go through the default path.
type escapeHatchFixture struct {
	Name string `json:"name"`
}

func TestWithWriterOverridesOnlyItsFormat(t *testing.T) {
	v := escapeHatchFixture{Name: "widget"}
	custom := func(w io.Writer) error {
		_, err := w.Write([]byte("# custom render for " + v.Name + "\n"))
		return err
	}

	var mdBuf bytes.Buffer
	if err := Render(&mdBuf, v, FormatMarkdown, WithWriter(FormatMarkdown, custom)); err != nil {
		t.Fatalf("Render markdown: %v", err)
	}
	if mdBuf.String() != "# custom render for widget\n" {
		t.Errorf("markdown = %q, want custom override output", mdBuf.String())
	}

	var textBuf bytes.Buffer
	if err := Render(&textBuf, v, FormatText, WithWriter(FormatMarkdown, custom)); err != nil {
		t.Fatalf("Render text: %v", err)
	}
	if textBuf.String() != "Name: widget\n" {
		t.Errorf("text = %q, want reflective default output", textBuf.String())
	}

	var jsonBuf bytes.Buffer
	if err := Render(&jsonBuf, v, FormatJSON, WithWriter(FormatMarkdown, custom)); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	if jsonBuf.String() != "{\n  \"name\": \"widget\"\n}\n" {
		t.Errorf("json = %q, want reflective default output", jsonBuf.String())
	}
}

func TestWithWriterErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	var buf bytes.Buffer
	err := Render(&buf, escapeHatchFixture{}, FormatText, WithWriter(FormatText, func(io.Writer) error { return boom }))
	if !errors.Is(err, boom) {
		t.Fatalf("Render error = %v, want wrapping %v", err, boom)
	}
}

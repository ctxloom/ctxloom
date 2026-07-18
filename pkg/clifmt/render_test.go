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

// escapeHatchFixture overrides only markdown rendering; every other format
// must still go through the reflective default.
type escapeHatchFixture struct {
	Name string `json:"name"`
}

func (f escapeHatchFixture) RenderCLI(w io.Writer, format Format) (bool, error) {
	if format != FormatMarkdown {
		return false, nil
	}
	_, err := w.Write([]byte("# custom render for " + f.Name + "\n"))
	return true, err
}

func TestRendererEscapeHatchOverridesOnlyDeclaredFormat(t *testing.T) {
	v := escapeHatchFixture{Name: "widget"}

	var mdBuf bytes.Buffer
	if err := Render(&mdBuf, v, FormatMarkdown); err != nil {
		t.Fatalf("Render markdown: %v", err)
	}
	if mdBuf.String() != "# custom render for widget\n" {
		t.Errorf("markdown = %q, want custom override output", mdBuf.String())
	}

	var textBuf bytes.Buffer
	if err := Render(&textBuf, v, FormatText); err != nil {
		t.Fatalf("Render text: %v", err)
	}
	if textBuf.String() != "Name: widget\n" {
		t.Errorf("text = %q, want reflective default output", textBuf.String())
	}

	var jsonBuf bytes.Buffer
	if err := Render(&jsonBuf, v, FormatJSON); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	if jsonBuf.String() != "{\n  \"name\": \"widget\"\n}\n" {
		t.Errorf("json = %q, want reflective default output", jsonBuf.String())
	}
}

func TestRendererEscapeHatchErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	v := failingRenderer{err: boom}
	var buf bytes.Buffer
	err := Render(&buf, v, FormatText)
	if !errors.Is(err, boom) {
		t.Fatalf("Render error = %v, want wrapping %v", err, boom)
	}
}

type failingRenderer struct{ err error }

func (f failingRenderer) RenderCLI(w io.Writer, format Format) (bool, error) {
	return true, f.err
}

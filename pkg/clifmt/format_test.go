package clifmt

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"json", FormatJSON, false},
		{"JSON", FormatJSON, false},
		{"  json  ", FormatJSON, false},
		{"yaml", FormatYAML, false},
		{"yml", FormatYAML, false},
		{"toml", FormatTOML, false},
		{"text", FormatText, false},
		{"txt", FormatText, false},
		{"markdown", FormatMarkdown, false},
		{"md", FormatMarkdown, false},
		{"xml", "", true},
		{"", "", true},
		{"csv", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseFormat(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseFormat(%q) = %v, nil; want error", tc.in, got)
				}
				if !errors.Is(err, ErrUnsupportedFormat) {
					t.Fatalf("ParseFormat(%q) error = %v; want wrapping ErrUnsupportedFormat", tc.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseFormat(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ParseFormat(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatIsValid(t *testing.T) {
	for _, f := range []Format{FormatJSON, FormatYAML, FormatTOML, FormatText, FormatMarkdown} {
		if !f.Valid() {
			t.Errorf("Format(%q).Valid() = false, want true", f)
		}
	}
	if Format("bogus").Valid() {
		t.Errorf("Format(%q).Valid() = true, want false", "bogus")
	}
}

// TestUnsupportedFormatErrorParity pins the shared behaviour of the three
// producers of clifmt's "unknown format" error — ParseFormat, Render, and any
// first-party CLI validating a Format it was handed (cmd/ltk's runCheck) —
// before they were collapsed onto one constructor. All three wrapped
// ErrUnsupportedFormat and all three spelled the supported list out by hand in
// a literal duplicated three times, once outside this package. The three
// literals were VERBATIM identical, so a parity test could not be red here;
// its job is to prove the collapse is behaviour-preserving and to make a future
// divergence fail.
func TestUnsupportedFormatErrorParity(t *testing.T) {
	_, parseErr := ParseFormat("xml")
	renderErr := Render(io.Discard, struct{}{}, Format("xml"))

	for name, err := range map[string]error{"ParseFormat": parseErr, "Render": renderErr} {
		if err == nil {
			t.Fatalf("%s(xml) returned no error", name)
		}
		if !errors.Is(err, ErrUnsupportedFormat) {
			t.Errorf("%s(xml) does not wrap ErrUnsupportedFormat: %v", name, err)
		}
	}
	if parseErr.Error() != renderErr.Error() {
		t.Errorf("the two producers disagree on the message:\nParseFormat: %s\nRender:      %s", parseErr, renderErr)
	}
	if got, want := parseErr.Error(), UnsupportedFormatError("xml").Error(); got != want {
		t.Errorf("ParseFormat's message %q is not the shared constructor's %q", got, want)
	}
	// The supported list must NAME every valid format, so adding a format
	// without updating the message cannot pass unnoticed.
	for _, f := range SupportedFormats() {
		if !strings.Contains(parseErr.Error(), string(f)) {
			t.Errorf("the supported list omits %q: %s", f, parseErr)
		}
	}
}

// TestFormatEnumerationsAgree pins that every place clifmt enumerates its
// format set answers consistently for the SAME format. The set used to be
// spelled out independently in the const block, Valid's switch, ParseFormat's
// switch, Render's switch and Structured's switch, with nothing failing if one
// were updated and another not. Green before the collapse onto a single table
// and green after; red if any one of the five ever stops covering a format the
// others cover.
func TestFormatEnumerationsAgree(t *testing.T) {
	formats := SupportedFormats()
	if len(formats) == 0 {
		t.Fatal("SupportedFormats() is empty")
	}
	structured := map[Format]bool{FormatJSON: true, FormatYAML: true, FormatTOML: true}

	for _, f := range formats {
		if !f.Valid() {
			t.Errorf("%q is in SupportedFormats but Valid() says no", f)
		}
		got, err := ParseFormat(string(f))
		if err != nil || got != f {
			t.Errorf("ParseFormat(%q) = (%q, %v); want (%q, nil)", f, got, err, f)
		}
		if err := Render(io.Discard, simpleFixture{Name: "n", Count: 1}, f); err != nil {
			t.Errorf("Render(%q) = %v; a supported format must dispatch", f, err)
		}
		if f.Structured() != structured[f] {
			t.Errorf("%q.Structured() = %v, want %v", f, f.Structured(), structured[f])
		}
	}

	if len(formats) != len(structured)+2 {
		t.Errorf("SupportedFormats() = %v; this test's structured/human split covers %d+2 formats and must be updated", formats, len(structured))
	}
	if Format("bogus").Valid() || Format("bogus").Structured() {
		t.Error("an unknown format must be neither valid nor structured")
	}
	for alias, want := range map[string]Format{"yml": FormatYAML, "txt": FormatText, "md": FormatMarkdown} {
		got, err := ParseFormat(alias)
		if err != nil || got != want {
			t.Errorf("ParseFormat(%q) = (%q, %v); want (%q, nil)", alias, got, err, want)
		}
	}
}

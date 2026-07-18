package clifmt

import (
	"errors"
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

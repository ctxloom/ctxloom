package clifmt

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ResolveFormat's table is cobrafmt.Resolve's cases with the flag lookup
// taken out: an unrequested format follows the terminal, an explicit one
// always wins, an explicit "" is text.
func TestResolveFormat(t *testing.T) {
	cases := []struct {
		requested          string
		explicit, terminal bool
		want               Format
	}{
		{"", false, true, FormatText},
		{"", false, false, FormatJSON},
		{"yaml", false, true, FormatText}, // a value not given explicitly is no request
		{"yaml", false, false, FormatJSON},
		{"json", true, true, FormatJSON},
		{"text", true, false, FormatText},
		{"", true, false, FormatText},
		{"", true, true, FormatText},
		{"yml", true, false, FormatYAML},
		{"MD", true, true, FormatMarkdown},
		{"toml", true, true, FormatTOML},
		{" txt ", true, false, FormatText},
	}
	for _, tc := range cases {
		got, err := ResolveFormat(tc.requested, tc.explicit, tc.terminal)
		if err != nil {
			t.Errorf("ResolveFormat(%q, %v, %v): %v", tc.requested, tc.explicit, tc.terminal, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ResolveFormat(%q, %v, %v) = %q, want %q", tc.requested, tc.explicit, tc.terminal, got, tc.want)
		}
	}
	if _, err := ResolveFormat("xml", true, true); !errors.Is(err, ErrUnsupportedFormat) {
		t.Errorf("an explicit unknown format: err = %v, want ErrUnsupportedFormat", err)
	}
	if f, err := ResolveFormat("xml", false, true); err != nil || f != FormatText {
		t.Errorf("an unrequested bad value is never parsed: got %q, %v", f, err)
	}
}

// FormatUsage names every supported format, in order, and says what an
// unset flag does.
func TestFormatUsage(t *testing.T) {
	want := "Output format: json, yaml, toml, text, or markdown (default: text on a terminal, json when output is piped or redirected)"
	if got := FormatUsage(); got != want {
		t.Errorf("FormatUsage() = %q, want %q", got, want)
	}
	for _, f := range SupportedFormats() {
		if !strings.Contains(FormatUsage(), string(f)) {
			t.Errorf("FormatUsage() omits %s", f)
		}
	}
}

type codeErr struct{ code int }

func (e codeErr) Error() string { return fmt.Sprintf("code %d", e.code) }
func (e codeErr) ExitCode() int { return e.code }

func TestExitCodeOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, 0},
		{"plain", errors.New("x"), 1},
		{"exit coder", codeErr{2}, 2},
		{"wrapped", fmt.Errorf("ctx: %w", codeErr{4}), 4},
		{"doubly wrapped", fmt.Errorf("a: %w", fmt.Errorf("b: %w", codeErr{5})), 5},
		{"joined, coder second", errors.Join(errors.New("x"), codeErr{6}), 6},
		{"joined, first coder wins", errors.Join(codeErr{7}, codeErr{8}), 7},
		{"outermost coder wins", codeErr{9}.wrapping(codeErr{10}), 9},
		{"exit status", ExitStatus{Code: 3}, 3},
		{"wrapped exit status", fmt.Errorf("x: %w", ExitStatus{Code: 0}), 0},
	}
	for _, tc := range cases {
		if got := ExitCodeOf(tc.err); got != tc.want {
			t.Errorf("%s: ExitCodeOf = %d, want %d", tc.name, got, tc.want)
		}
	}
}

type wrappingCodeErr struct {
	codeErr
	inner error
}

func (e wrappingCodeErr) Unwrap() error { return e.inner }

func (e codeErr) wrapping(inner error) error { return wrappingCodeErr{e, inner} }

func TestExitStatus(t *testing.T) {
	var ec ExitCoder = ExitStatus{Code: 3}
	if ec.ExitCode() != 3 {
		t.Errorf("ExitCode = %d", ec.ExitCode())
	}
	if got := (ExitStatus{Code: 3}).Error(); got != "exit code 3" {
		t.Errorf("Error() = %q", got)
	}
}

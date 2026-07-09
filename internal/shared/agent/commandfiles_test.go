package agent

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSafeCommandRelPath pins the path-traversal guard the per-agent
// command/skill writers use for names and manifest lines: absolute paths and
// any ".." element are rejected; plain and nested relative names pass and
// join under dir.
func TestSafeCommandRelPath(t *testing.T) {
	dir := filepath.Join("/work", ".agents", "skills")

	tests := []struct {
		name string
		in   string
		ok   bool
		want string // joined path when ok
	}{
		{"plain name", "good.md", true, filepath.Join(dir, "good.md")},
		{"nested name", "group/cmd.md", true, filepath.Join(dir, "group", "cmd.md")},
		{"empty", "", false, ""},
		{"absolute", "/abs/path.md", false, ""},
		{"parent traversal", "../escape.md", false, ""},
		{"nested traversal", "a/../../b.md", false, ""},
		{"interior dotdot", "a/../b.md", false, ""},
		{"bare dotdot", "..", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SafeCommandRelPath(dir, tt.in)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestEscapeYAMLString pins the frontmatter scalar escaper: plain strings pass
// through unquoted; quote-triggering strings are double-quoted with backslash
// escaped before the quote (so a value like C:\path round-trips instead of
// hitting an invalid \p escape); and type-ambiguous scalars (bool/null/number)
// are quoted so they stay strings.
func TestEscapeYAMLString(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain stays unquoted", "hello world", "hello world"},
		{"lone backslash stays unquoted", `foo\bar`, `foo\bar`},
		{"colon forces quoting", "foo: bar", `"foo: bar"`},
		{"colon plus backslash escapes backslash", `C:\path`, `"C:\\path"`},
		{"backslash and quote both escaped", `a:"b\c`, `"a:\"b\\c"`},
		{"true is quoted", "true", `"true"`},
		{"null is quoted", "null", `"null"`},
		{"YAML1.1 off is quoted", "off", `"off"`},
		{"tilde null is quoted", "~", `"~"`},
		{"integer is quoted", "123", `"123"`},
		{"float is quoted", "1.0", `"1.0"`},
		{"signed number is quoted", "-5", `"-5"`},
		{"leading block indicator is quoted", "- item", `"- item"`},
		{"non-literal word stays unquoted", "description", "description"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EscapeYAMLString(tt.in))
		})
	}
}

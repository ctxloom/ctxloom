package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncateBytes(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{"under cap", "hello", 10, "hello"},
		{"exact cap", "hello", 5, "hello"},
		{"ascii cut", "hello world", 5, "hello"},
		{"zero", "hello", 0, ""},
		{"negative", "hello", -1, ""},
		// "héllo": é is 2 bytes (0xC3 0xA9). A 2-byte cut lands mid-é and must
		// back off to "h".
		{"mid multibyte rune backs off", "héllo", 2, "h"},
		// Cutting just after the full é (3 bytes) keeps it.
		{"after multibyte rune", "héllo", 3, "hé"},
		// Emoji is 4 bytes; cutting inside it drops the whole rune.
		{"mid emoji backs off", "a😀b", 3, "a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateBytes(tt.in, tt.maxBytes)
			if got != tt.want {
				t.Errorf("TruncateBytes(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateBytes(%q, %d) = %q is not valid UTF-8", tt.in, tt.maxBytes, got)
			}
			if len(got) > tt.maxBytes && tt.maxBytes > 0 {
				t.Errorf("TruncateBytes(%q, %d) = %q exceeds cap", tt.in, tt.maxBytes, got)
			}
		})
	}
}

package textutil

import (
	"strings"
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

// The backoff exists to drop the TRAILING BYTES OF A SPLIT RUNE, and a split
// rune is at most utf8.UTFMax-1 = 3 bytes. Unbounded, the same loop walks
// straight through genuinely invalid input — a byte per iteration — eating real
// content and, when the whole capped prefix is invalid, returning "" for a
// string that had plenty in it. A zero-payload success is the worst answer
// available: an error message rendering it as %q then reports that the program
// produced NOTHING, which is a different and false diagnosis.
func TestTruncateBytes_BacksOffAtMostASplitRune(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxBytes int
		want     string
	}{
		{
			name:     "an all-invalid prefix is not annihilated",
			in:       strings.Repeat("\xff", 64),
			maxBytes: 16,
			want:     strings.Repeat("\xff", 16),
		},
		{
			name:     "invalid trailing bytes do not eat the valid content before them",
			in:       "abc\xff\xff\xff\xffxx",
			maxBytes: 7,
			want:     "abc\xff\xff\xff\xff",
		},
		// The three cases the backoff is FOR, at every multibyte width, must
		// keep working: 2-byte, 3-byte and 4-byte runes each split one byte
		// short of whole.
		{name: "2-byte rune split", in: "h\u00e9llo", maxBytes: 2, want: "h"},
		{name: "3-byte rune split", in: "a\u65e5b", maxBytes: 3, want: "a"},
		{name: "4-byte rune split one byte short", in: "a\U0001F600b", maxBytes: 4, want: "a"},
		// A legitimately encoded U+FFFD decodes with size 3 and is content,
		// not a partial sequence.
		{name: "a real replacement char survives", in: "a\ufffdbc", maxBytes: 4, want: "a\ufffd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateBytes(tt.in, tt.maxBytes)
			if got != tt.want {
				t.Errorf("TruncateBytes(%q, %d) = %q, want %q", tt.in, tt.maxBytes, got, tt.want)
			}
			if len(got) > tt.maxBytes {
				t.Errorf("TruncateBytes(%q, %d) = %q exceeds cap", tt.in, tt.maxBytes, got)
			}
			if tt.in != "" && tt.maxBytes >= utf8.UTFMax && got == "" {
				t.Errorf("TruncateBytes(%q, %d) returned nothing for a non-empty input", tt.in, tt.maxBytes)
			}
		})
	}
}

// The bound is what makes the work O(1) rather than O(k) in the length of an
// invalid run: whatever the input, at most three bytes are ever dropped.
func TestTruncateBytes_DropsAtMostThreeBytes(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("\xff", 200),
		"ok" + strings.Repeat("\xfe", 100),
		strings.Repeat("a\U0001F600", 40),
	} {
		for _, max := range []int{4, 17, 63} {
			got := TruncateBytes(in, max)
			if len(got) < max-(utf8.UTFMax-1) {
				t.Errorf("TruncateBytes(%q…, %d) backed off %d bytes, more than a split rune can be", in[:4], max, max-len(got))
			}
		}
	}
}

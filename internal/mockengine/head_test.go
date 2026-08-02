package mockengine

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// head is documented as returning a bounded, PRINTABLE prefix. It sliced at a
// fixed byte offset and filtered nothing, so it could hand back half a UTF-8
// rune and could carry raw control bytes straight out of a binary surface into
// the report a human reads.
func TestHead_NeverSplitsARune(t *testing.T) {
	// Three-byte runes: 256 is not a multiple of 3, so a byte-offset cut lands
	// mid-rune.
	body := []byte(strings.Repeat("字", 200))
	got := head(body)

	if !utf8.ValidString(got) {
		t.Errorf("head returned invalid UTF-8 — a rune was cut in half: %q", got)
	}
	if len(got) > headLimit {
		t.Errorf("head returned %d bytes, over the %d limit", len(got), headLimit)
	}
	if len(got) == 0 {
		t.Error("head returned nothing for a 600-byte input")
	}
	// It must still be a genuine PREFIX of the input, not a re-encoding.
	if !strings.HasPrefix(string(body), got) {
		t.Errorf("head is no longer a prefix of its input: %q", got)
	}
}

// Raw control bytes from a binary surface must not ride into the report as-is.
func TestHead_ReplacesControlBytes(t *testing.T) {
	got := head([]byte("ok\x00\x1b[31mred\x07"))
	for _, r := range got {
		switch r {
		case '\n', '\t':
			continue
		case 0x00, 0x1b, 0x07:
			t.Errorf("head carried raw control byte %#x into a prefix documented as printable: %q", r, got)
		}
	}
	if !strings.HasPrefix(got, "ok") {
		t.Errorf("the readable part was lost: %q", got)
	}
}

// Ordinary text — the overwhelmingly common case — is passed through
// byte-for-byte, newlines and tabs included.
func TestHead_TextIsByteStable(t *testing.T) {
	for _, s := range []string{
		"",
		"# Project rules\nAlways run the tests.\n",
		"a\tb\nc",
		"héllo wörld — em dash and ünïcode",
	} {
		if got := head([]byte(s)); got != s {
			t.Errorf("head(%q) = %q, want it unchanged", s, got)
		}
	}
}

// Invalid bytes that are NOT a truncation are still reported, as the
// replacement character rather than as raw bytes.
func TestHead_InvalidBytesBecomeReplacementChars(t *testing.T) {
	got := head([]byte{'a', 0xff, 0xfe, 'b'})
	if !utf8.ValidString(got) {
		t.Errorf("head returned invalid UTF-8: %q", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") {
		t.Errorf("the readable bytes were lost: %q", got)
	}
}

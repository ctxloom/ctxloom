package tokens

import "testing"

func TestEstimate(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},      // 4 chars / 4
		{"abcdefghi", 2}, // 9 chars / 4 = 2 (floor)
	}
	for _, tt := range tests {
		if got := Estimate(tt.in); got != tt.want {
			t.Errorf("Estimate(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestEstimate_CountsBytesNotCharacters pins what the constant's name now
// claims. The heuristic has always measured bytes — len() over a string counts
// bytes — while the name and doc said characters, so for non-ASCII text every
// consumer reading the documented meaning was wrong by 2-4x. Pinning the byte
// reading makes a later switch to characters (or to a real tokenizer) a change
// someone has to make deliberately, rather than one that slips in behind a
// name that already promised it.
func TestEstimate_CountsBytesNotCharacters(t *testing.T) {
	// Four CJK characters are twelve bytes in UTF-8: three tokens on the byte
	// reading, one on the character reading. The two readings genuinely
	// disagree here, which is what makes this a pin rather than a restatement.
	const fourCJKChars = "世界之心"
	if got, want := len(fourCJKChars), 12; got != want {
		t.Fatalf("fixture: len(%q) = %d bytes, want %d — the fixture must be multi-byte "+
			"or this test cannot tell the two readings apart", fourCJKChars, got, want)
	}
	if got, want := Estimate(fourCJKChars), 12/BytesPerToken; got != want {
		t.Errorf("Estimate(%q) = %d, want %d (bytes/%d, not characters/%d)",
			fourCJKChars, got, want, BytesPerToken, BytesPerToken)
	}
}

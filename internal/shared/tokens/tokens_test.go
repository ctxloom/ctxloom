package tokens

import (
	"strings"
	"testing"
)

func TestEstimate(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},      // 4 bytes / 4
		{"abcdefghi", 3}, // 9 bytes / 4, rounded UP — see
		//                   TestEstimate_NonEmptyTextIsNeverZeroTokens for why
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

// TestEstimate_NonEmptyTextIsNeverZeroTokens pins the rounding. Integer
// division returned 0 for anything shorter than BytesPerToken, so a non-empty
// string estimated at zero tokens — a trap for every caller that gates on the
// result: a budget check that concludes there is nothing to send, a "does this
// need chunking" test, a ratio with the estimate in the denominator. Only
// genuinely empty text may estimate at zero.
func TestEstimate_NonEmptyTextIsNeverZeroTokens(t *testing.T) {
	if got := Estimate(""); got != 0 {
		t.Errorf("Estimate(%q) = %d, want 0 — empty text is the ONE case that may be zero", "", got)
	}
	for _, in := range []string{"a", "ab", "abc", "abcd", "世"} {
		if got := Estimate(in); got < 1 {
			t.Errorf("Estimate(%q) = %d, want at least 1: non-empty text must never estimate at zero", in, got)
		}
	}
}

// TestBudget_IsTheInverseOfEstimate pins the seam this package's own doc
// promises: "a real tokenizer can replace the heuristic here without touching
// call sites". That held for Estimate and failed for the constant, which a
// consumer multiplied by directly to size a chunk from a token budget — a
// second copy of the heuristic living outside the package that owns it, and
// one no real tokenizer could satisfy, since a tokenizer is not invertible by
// multiplication.
//
// Budget is the inverse direction expressed as a function, so the substitution
// point is inside this package for both directions. Round-tripping is the
// property that makes it an inverse rather than a coincidence.
func TestBudget_IsTheInverseOfEstimate(t *testing.T) {
	if got := Budget(0); got != 0 {
		t.Errorf("Budget(0) = %d, want 0", got)
	}
	if got := Budget(-1); got != 0 {
		t.Errorf("Budget(-1) = %d, want 0: a negative budget is not a size", got)
	}
	for _, want := range []int{1, 2, 8, 500, 8000} {
		size := Budget(want)
		if size <= 0 {
			t.Fatalf("Budget(%d) = %d, want a positive size", want, size)
		}
		if got := Estimate(strings.Repeat("x", size)); got != want {
			t.Errorf("Estimate(Budget(%d)) = %d, want %d — Budget must be Estimate's inverse", want, got, want)
		}
	}
}

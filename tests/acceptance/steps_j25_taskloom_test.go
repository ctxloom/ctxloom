//go:build acceptance

package acceptance

import "testing"

// TestParseTaskloomAddHarp_ExtractsFirstTabField pins that "parse the
// harp id out of `taskloom add` stdout" was duplicated verbatim at two call
// sites (steps_j25_taskloom.go:99-103 and :199-203) instead of sharing one
// implementation.
func TestParseTaskloomAddHarp_ExtractsFirstTabField(t *testing.T) {
	got, err := parseTaskloomAddHarp("brave-mango-falcon\tAdded task \"foo\"\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "brave-mango-falcon" {
		t.Fatalf("got %q, want %q", got, "brave-mango-falcon")
	}
}

// TestParseTaskloomAddHarp_EmptyOutputErrors pins the failure path both
// call sites relied on.
func TestParseTaskloomAddHarp_EmptyOutputErrors(t *testing.T) {
	if _, err := parseTaskloomAddHarp("   \n"); err == nil {
		t.Fatal("expected an error for empty/whitespace-only output")
	}
}

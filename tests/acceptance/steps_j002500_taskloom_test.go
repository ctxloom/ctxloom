//go:build acceptance

package acceptance

import "testing"

// TestParseTaskloomAddHarp_ExtractsHarpIDField pins that "parse the harp id
// out of `taskloom add` stdout" is shared by both call sites
// (steps_j002500_taskloom.go's "taskloom has tasks:" and "taskloom adds a
// task ...") instead of duplicated verbatim. taskloom shares ctxloom's
// cliemit.Resolve, so its stdout off a terminal (this harness, always) is the
// JSON tasks.Task object — not the tab-separated text line this used to
// parse before that default changed.
func TestParseTaskloomAddHarp_ExtractsHarpIDField(t *testing.T) {
	got, err := parseTaskloomAddHarp(`{"harp_id":"brave-mango-falcon","text":"foo"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "brave-mango-falcon" {
		t.Fatalf("got %q, want %q", got, "brave-mango-falcon")
	}
}

// TestParseTaskloomAddHarp_EmptyOutputErrors pins the failure path both
// call sites relied on: invalid JSON.
func TestParseTaskloomAddHarp_EmptyOutputErrors(t *testing.T) {
	if _, err := parseTaskloomAddHarp("   \n"); err == nil {
		t.Fatal("expected an error for empty/whitespace-only output")
	}
}

// TestParseTaskloomAddHarp_MissingHarpIDErrors pins the failure path for
// well-formed JSON that simply carries no harp_id — a silent empty string
// would otherwise be mistaken for a real harp downstream.
func TestParseTaskloomAddHarp_MissingHarpIDErrors(t *testing.T) {
	if _, err := parseTaskloomAddHarp(`{"text":"foo"}`); err == nil {
		t.Fatal("expected an error for a payload with no harp_id")
	}
}

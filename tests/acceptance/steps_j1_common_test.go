//go:build acceptance

package acceptance

import "testing"

// TestPromptSection_MissingMarkerFailsLoud pins U159-F06: promptSection used
// to return "" when the "=== Prompt ===" marker was absent from the mock's
// recorded input — a mock that recorded something other than a prompt (or a
// record-file format change) then looked identical to "the prompt really is
// empty", and every caller's assertion failed with a confusing "prompt does
// not contain X; prompt:" dump of nothing. The marker's absence is a
// harness/product break, not an empty prompt, so it must fail loud instead.
func TestPromptSection_MissingMarkerFailsLoud(t *testing.T) {
	_, err := promptSection("no marker anywhere in this recorded text")
	if err == nil {
		t.Fatal("expected an error when the \"=== Prompt ===\" marker is absent, got nil")
	}
}

// TestPromptSection_ExtractsAfterMarker is the ordinary success path.
func TestPromptSection_ExtractsAfterMarker(t *testing.T) {
	got, err := promptSection("=== Env ===\nFOO=bar\n=== Prompt ===\nhello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "hello world" {
		t.Fatalf("promptSection = %q, want %q", got, "hello world")
	}
}

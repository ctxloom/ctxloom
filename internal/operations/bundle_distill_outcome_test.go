package operations

import "testing"

// TestDistillOutcome_FailureReportedSkipped is a regression guard: a per-item
// distill failure leaves DistilledBy empty (distillFragments warns and skips on
// error). Such an item must be reported skipped/failed, NOT distilled with an
// empty model id — the previous code stamped every target "distilled",
// silently lying in the summary.
func TestDistillOutcome_FailureReportedSkipped(t *testing.T) {
	failed := distillOutcome(ItemKindFragment, "frag", "")
	if failed.Status != DistillStatusSkipped {
		t.Fatalf("empty DistilledBy must be Skipped, got %q", failed.Status)
	}
	if failed.Reason != "distill_failed" {
		t.Fatalf("expected reason distill_failed, got %q", failed.Reason)
	}
	if failed.ModelID != "" {
		t.Fatalf("failed item must carry no model id, got %q", failed.ModelID)
	}

	ok := distillOutcome(ItemKindPrompt, "p", "claude-haiku-4-5")
	if ok.Status != DistillStatusDistilled {
		t.Fatalf("stamped DistilledBy must be Distilled, got %q", ok.Status)
	}
	if ok.ModelID != "claude-haiku-4-5" {
		t.Fatalf("expected model id passthrough, got %q", ok.ModelID)
	}
}

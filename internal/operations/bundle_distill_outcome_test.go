package operations

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestDistillOutcome_FailureReportedSkipped is a regression guard: a per-item
// distill failure leaves DistilledBy empty (distillFragments warns and skips on
// error). Such an item must be reported skipped/failed, NOT distilled with an
// empty model id — the previous code stamped every target "distilled",
// silently lying in the summary.
func TestDistillOutcome_FailureReportedSkipped(t *testing.T) {
	failed := distillOutcome(ItemKindFragment, "frag", "body", "", false)
	if failed.Status != DistillStatusSkipped {
		t.Fatalf("empty DistilledBy must be Skipped, got %q", failed.Status)
	}
	if failed.Reason != "distill_failed" {
		t.Fatalf("expected reason distill_failed, got %q", failed.Reason)
	}
	if failed.ModelID != "" {
		t.Fatalf("failed item must carry no model id, got %q", failed.ModelID)
	}

	ok := distillOutcome(ItemKindCommand, "p", "body", "claude-haiku-4-5", false)
	if ok.Status != DistillStatusDistilled {
		t.Fatalf("stamped DistilledBy must be Distilled, got %q", ok.Status)
	}
	if ok.ModelID != "claude-haiku-4-5" {
		t.Fatalf("expected model id passthrough, got %q", ok.ModelID)
	}

	// The RE-distill failure case: the old DistilledBy survives the failed
	// attempt, so the failed flag — not post-state — must drive the outcome.
	redistillFailed := distillOutcome(ItemKindFragment, "frag", "body", "stale-model", true)
	if redistillFailed.Status != DistillStatusSkipped || redistillFailed.Reason != "distill_failed" {
		t.Fatalf("failed re-distill must be reported distill_failed, got %+v", redistillFailed)
	}
	if redistillFailed.ModelID != "" {
		t.Fatalf("failed re-distill must not report the stale model id, got %q", redistillFailed.ModelID)
	}
}

// errDistiller always fails.
type errDistiller struct{}

func (errDistiller) Distill(context.Context, DistillRequest) (DistillResult, error) {
	return DistillResult{}, fmt.Errorf("llm unavailable")
}

// TestDistillBundleFile_FailedRedistillReported is the end-to-end regression
// for the stale-model lie: a fragment that was distilled before (DistilledBy
// set) but whose content changed (NeedsDistill) is re-attempted; when the
// attempt fails, the old Distilled/DistilledBy stay in the bundle, and the
// outcome must still be distill_failed — not "distilled" with the stale id.
func TestDistillBundleFile_FailedRedistillReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bundle.yaml")
	// content_hash deliberately does NOT match the content, so NeedsDistill
	// is true (a re-distill) while distilled/distilled_by carry stale values.
	bundleYAML := `version: 1.0.0
fragments:
  rules:
    content: "fresh content the stale distillation no longer matches"
    distilled: "stale distilled text"
    distilled_by: "stale-model"
    content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: errDistiller{},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)

	item := res.Items[0]
	assert.Equal(t, "rules", item.Name)
	assert.Equal(t, DistillStatusSkipped, item.Status,
		"a failed re-distill must not be reported as distilled")
	assert.Equal(t, "distill_failed", item.Reason)
	assert.Empty(t, item.ModelID, "the stale model id must not be reported as a fresh success")
}

// okDistiller always succeeds with a fixed distilled body.
type okDistiller struct{ body string }

func (d okDistiller) Distill(context.Context, DistillRequest) (DistillResult, error) {
	return DistillResult{Distilled: d.body, ModelID: "fake-model"}, nil
}

// TestDistillBundleFile_InvalidatesPriorApproval is the re-distill LOUD PATH
// (spec §10.4) end to end: a fragment with a PRIOR approve countersignature
// over its distilled bytes is re-distilled to DIFFERENT bytes; the old
// approval necessarily no longer covers the new bytes, so the file result
// must report the item invalidated.
func TestDistillBundleFile_InvalidatesPriorApproval(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "mybundle.yaml")
	bundleYAML := `version: 1.0.0
fragments:
  rules:
    content: "fresh content the stale distillation no longer matches"
    distilled: "old distilled text"
    distilled_by: "stale-model"
    content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	// Seed a prior UNSIGNED approve countersignature over the OLD distilled
	// bytes (spec §9.5's degraded path; unsigned is sufficient here — the
	// invalidation check only asks "did ANY prior approve record exist",
	// which the sidecar index answers regardless of signedness).
	ref := trust.Ref{Bundle: "mybundle", Kind: trust.KindFragment, Name: "rules", IsLocal: true}
	home, err := paths.HomeApprovalsPath()
	require.NoError(t, err)
	store := countersign.NewStore(home, afero.NewOsFs())
	require.NoError(t, store.WriteUnsignedApprove(countersignRef(ref), signing.AttestFragmentDistilled, []byte("old distilled text")))
	require.NoError(t, store.AppendIndex(countersign.IndexEntry{
		Ref: countersignRef(ref), Kind: string(trust.KindFragment), Form: string(signing.FormDistilled),
		Assertion: string(signing.AssertionApprove), Unsigned: true, PayloadHash: "sha256:old", ReviewedAt: "2026-01-01T00:00:00Z",
	}))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: okDistiller{body: "brand new distilled text"},
	})
	require.NoError(t, err)
	require.Len(t, res.Items, 1)
	require.Equal(t, DistillStatusDistilled, res.Items[0].Status)
	assert.Equal(t, []string{"fragment/rules"}, res.Invalidated,
		"a prior approval over the distilled form must be reported invalidated")
}

// TestDistillBundleFile_NoInvalidationWhenNeverApproved proves the loud path
// is silent when nothing was ever approved — no false alarms.
func TestDistillBundleFile_NoInvalidationWhenNeverApproved(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "mybundle.yaml")
	bundleYAML := `version: 1.0.0
fragments:
  rules:
    content: "fresh content the stale distillation no longer matches"
    distilled: "old distilled text"
    distilled_by: "stale-model"
    content_hash: "0000000000000000000000000000000000000000000000000000000000000000"
`
	require.NoError(t, os.WriteFile(path, []byte(bundleYAML), 0o644))

	res, err := DistillBundleFile(context.Background(), DistillBundleFileRequest{
		Path:      path,
		Distiller: okDistiller{body: "brand new distilled text"},
	})
	require.NoError(t, err)
	assert.Empty(t, res.Invalidated)
}

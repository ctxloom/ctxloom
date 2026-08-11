package operations

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func TestWriteAndRecordSyncSummary_NilResultIsNoop(t *testing.T) {
	var buf bytes.Buffer
	WriteAndRecordSyncSummary(&buf, nil)
	assert.Empty(t, buf.String(), "nil result must not produce output — callers shouldn't have to nil-check")
}

func TestWriteAndRecordSyncSummary_UpToDateIsSilent(t *testing.T) {
	var buf bytes.Buffer
	WriteAndRecordSyncSummary(&buf, &SyncDependenciesResult{
		Status:  "up_to_date",
		Total:   3,
		Message: "all bundles up to date",
	})
	assert.Empty(t, buf.String(),
		"up_to_date with no installs/updates is quiet — startup shouldn't spam stderr in the steady state")
}

func TestWriteAndRecordSyncSummary_InstalledOrUpdatedPrintsMessage(t *testing.T) {
	var buf bytes.Buffer
	WriteAndRecordSyncSummary(&buf, &SyncDependenciesResult{
		Status:    "synced",
		Installed: 2,
		Updated:   1,
		Message:   "installed 2, updated 1",
	})

	out := buf.String()
	assert.Contains(t, out, "ctxloom: installed 2, updated 1",
		"successful work should be summarized with the ctxloom: prefix")
	assert.NotContains(t, out, "warning",
		"a clean install/update is not a warning")
}

func TestWriteAndRecordSyncSummary_FailuresListEachFailedItem(t *testing.T) {
	resetStrictness(t) // WriteAndRecordSyncSummary records a finding per failed item; keep them out of the shared collector
	var buf bytes.Buffer
	WriteAndRecordSyncSummary(&buf, &SyncDependenciesResult{
		Status: "partial",
		Errors: 2,
		Failed: []SyncItem{
			{
				Reference: "myorg/parent-profile",
				Type:      "profile",
				Status:    "failed",
				Error:     "remote not found in registry",
			},
			{
				Reference: "myorg/repo@v1/bundle-name",
				Type:      "bundle",
				Status:    "failed",
				Error:     "clone failed: authentication required",
			},
		},
	})

	out := buf.String()
	// Header line
	assert.Contains(t, out, "ctxloom: warning: sync completed with 2 errors",
		"failure header must use the warning prefix and the count")

	// Per-item lines — this is the visibility surface for the hard-fail
	// behavior change. Without these, users see only the count.
	assert.Contains(t, out, "myorg/parent-profile")
	assert.Contains(t, out, "(profile)")
	assert.Contains(t, out, "remote not found in registry")

	assert.Contains(t, out, "myorg/repo@v1/bundle-name")
	assert.Contains(t, out, "(bundle)")
	assert.Contains(t, out, "clone failed: authentication required")

	// Indent convention: failures sit under the header with "  - " markers.
	indentedLines := 0
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "ctxloom:   - ") {
			indentedLines++
		}
	}
	assert.Equal(t, 2, indentedLines,
		"each failed item must get its own indented line so diagnosis is unambiguous")

	// Each failed item is ALSO recorded as a fatal sync finding for the strict
	// startup gate (a pinned/configured item neither cached nor fetchable) — so a
	// hard-fail sync aborts `ctxloom run`/`mcp` rather than silently degrading.
	findings := strictness.All()
	require.Len(t, findings, 2, "each failed sync item records a fatal finding")
	for _, f := range findings {
		assert.Equal(t, strictness.ClassSync, f.Class, "a failed sync item is sync-class")
		assert.NotEmpty(t, f.FixIt, "the finding carries a fix-it hint")
	}
}

func TestWriteAndRecordSyncSummary_InstalledAndErrorsBothPrinted(t *testing.T) {
	// Partial-success path: per CLAUDE.md, 9 of 10 succeeding is still success.
	// The user needs to see *both* what worked and what didn't.
	resetStrictness(t) // the failed item records a finding; keep it out of the shared collector
	var buf bytes.Buffer
	WriteAndRecordSyncSummary(&buf, &SyncDependenciesResult{
		Status:    "partial",
		Installed: 9,
		Errors:    1,
		Message:   "installed 9, 1 failed",
		Failed: []SyncItem{
			{Reference: "myorg/broken", Type: "bundle", Error: "network timeout"},
		},
	})

	out := buf.String()
	assert.Contains(t, out, "ctxloom: installed 9, 1 failed",
		"partial-success summary should still surface what got installed")
	assert.Contains(t, out, "warning: sync completed with 1 errors",
		"partial-success summary must still warn about the failures")
	assert.Contains(t, out, "myorg/broken",
		"partial-success summary must still name failed items")

	// The single failed item is still recorded as a fatal sync finding even on the
	// partial-success path (installs succeeded, but the missing pinned item is fatal).
	findings := strictness.All()
	require.Len(t, findings, 1, "the failed item records a fatal sync finding despite partial success")
	assert.Equal(t, strictness.ClassSync, findings[0].Class)
	assert.Contains(t, findings[0].Message, "myorg/broken", "the finding names the failed item")
}

func TestReportCompanions_PresentBinariesLogVersions(t *testing.T) {
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreProbe := config.SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return []byte(`{"name":"x","version":"v9.9.9"}`), nil
	})
	defer restoreProbe()
	var buf bytes.Buffer

	ReportCompanions(&buf)

	assert.Contains(t, buf.String(), "ctxloom: companion taskloom v9.9.9")
	assert.Contains(t, buf.String(), "ctxloom: companion ltk v9.9.9")
}

func TestReportCompanions_MissingBinariesStaySilent(t *testing.T) {
	restoreLook := config.SetLookPathForTesting(func(string) (string, error) {
		return "", errors.New("not found")
	})
	defer restoreLook()
	var buf bytes.Buffer

	ReportCompanions(&buf)

	assert.Empty(t, buf.String(), "install hints belong to the bundle resolvers, not the boot report")
}

func TestReportCompanions_ProbeFailureWarnsButContinues(t *testing.T) {
	defer config.AdmitEveryDiscoveredCompanionForTesting()()
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreProbe := config.SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return nil, errors.New("exec format error")
	})
	defer restoreProbe()
	var buf bytes.Buffer

	ReportCompanions(&buf)

	assert.Contains(t, buf.String(), "ctxloom: warning: companion taskloom")
	assert.Contains(t, buf.String(), "ctxloom: warning: companion ltk")
}

// TestSweepOrphanedWorktrees_SilentWhenNothingToReap covers the plumbing
// (this file's call into isolation.ReapOrphanedWorktrees, and the
// nothing-found reporting contract) — the reap/spare/skip SEMANTICS
// themselves (a confirmed-dead owner's clean worktree reaped, a dirty one
// spared, a live or indeterminate owner never touched) are proven directly
// against real git in internal/lm/isolation's own TestReapOrphanedWorktrees_*
// suite, not duplicated here.
func TestSweepOrphanedWorktrees_SilentWhenNothingToReap(t *testing.T) {
	testsupport.Isolate(t) // fresh, empty ~/.ctxloom/sessions — nothing to sweep
	var buf bytes.Buffer

	SweepOrphanedWorktrees(context.Background(), &buf)

	assert.Empty(t, buf.String(), "an all-clear sweep reports nothing")
}

package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestLoadConfigOrFallback_Success(t *testing.T) {
	want := &config.Config{AppPaths: []string{"/some/dir"}}
	var buf bytes.Buffer

	got := loadConfigOrFallback(func() (*config.Config, error) {
		return want, nil
	}, &buf)

	assert.Same(t, want, got, "should return loader's config unchanged on success")
	assert.Empty(t, buf.String(), "no warning on success")
}

func TestLoadConfigOrFallback_FailureReturnsMinimalDefault(t *testing.T) {
	var buf bytes.Buffer

	got := loadConfigOrFallback(func() (*config.Config, error) {
		return nil, errors.New("config: missing required field")
	}, &buf)

	assert.NotNil(t, got)
	assert.Equal(t, []string{".ctxloom"}, got.AppPaths,
		"fallback config must be rooted at .ctxloom so downstream operations have somewhere to look")

	out := buf.String()
	assert.Contains(t, out, "ctxloom: warning:",
		"warning must use the project-standard ctxloom: warning: prefix")
	assert.Contains(t, out, "failed to load config",
		"warning must explain what failed")
	assert.Contains(t, out, "config: missing required field",
		"warning must include the underlying error for diagnosis")
	assert.Contains(t, out, ".ctxloom",
		"warning must tell the user where the fallback is rooted")
}

func TestWriteSyncSummary_NilResultIsNoop(t *testing.T) {
	var buf bytes.Buffer
	writeSyncSummary(&buf, nil)
	assert.Empty(t, buf.String(), "nil result must not produce output — callers shouldn't have to nil-check")
}

func TestWriteSyncSummary_UpToDateIsSilent(t *testing.T) {
	var buf bytes.Buffer
	writeSyncSummary(&buf, &operations.SyncDependenciesResult{
		Status:  "up_to_date",
		Total:   3,
		Message: "all bundles up to date",
	})
	assert.Empty(t, buf.String(),
		"up_to_date with no installs/updates is quiet — startup shouldn't spam stderr in the steady state")
}

func TestWriteSyncSummary_InstalledOrUpdatedPrintsMessage(t *testing.T) {
	var buf bytes.Buffer
	writeSyncSummary(&buf, &operations.SyncDependenciesResult{
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

func TestWriteSyncSummary_FailuresListEachFailedItem(t *testing.T) {
	var buf bytes.Buffer
	writeSyncSummary(&buf, &operations.SyncDependenciesResult{
		Status: "partial",
		Errors: 2,
		Failed: []operations.SyncItem{
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
}

func TestWriteSyncSummary_InstalledAndErrorsBothPrinted(t *testing.T) {
	// Partial-success path: per CLAUDE.md, 9 of 10 succeeding is still success.
	// The user needs to see *both* what worked and what didn't.
	var buf bytes.Buffer
	writeSyncSummary(&buf, &operations.SyncDependenciesResult{
		Status:    "partial",
		Installed: 9,
		Errors:    1,
		Message:   "installed 9, 1 failed",
		Failed: []operations.SyncItem{
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
}

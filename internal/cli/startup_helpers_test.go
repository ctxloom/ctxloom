package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// resetStrictness restores pristine strict-mode state for a test and registers
// cleanup, so the package-global finding collector never bleeds between tests
// (mirrors strictness_test.go's resetForTest).
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// The strict startup gate is the whole point of fail-loudly: a collected fatal
// finding must abort `ctxloom run`/`mcp` before launch with the distinct
// findings exit code (3), listing every finding and its fix-it plus the
// --degraded escape hatch. In degraded mode the same finding must NOT abort.
func TestFailOnFindings_StrictAbortsWithFindingsExitCode(t *testing.T) {
	t.Run("strict mode aborts, lists finding + fix-it", func(t *testing.T) {
		resetStrictness(t)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		require.Error(t, err, "a collected fatal finding must abort startup in strict mode")
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr, "the abort must be an ExitError so deferred cleanup runs")
		assert.Equal(t, exitCodeFatalFindings, exitErr.Code,
			"strict abort must carry the distinct findings exit code (3), not the generic 1")

		out := buf.String()
		assert.Contains(t, out, "aborting startup", "header must announce the abort")
		assert.Contains(t, out, "[sync]", "each finding is class-tagged for diagnosis")
		assert.Contains(t, out, "sync failed: boom", "the finding message must be listed")
		assert.Contains(t, out, "check the remote/network", "the fix-it must be listed")
		assert.Contains(t, out, "--degraded", "the escape hatch must be surfaced")
	})

	t.Run("degraded mode never aborts", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassSync,
			"check the remote/network, or pass --degraded to launch anyway",
			"sync failed: %v", "boom")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		assert.NoError(t, err, "degraded mode is the escape hatch — it must launch anyway")
		assert.Empty(t, buf.String(), "degraded mode renders no findings block")
	})
}

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

// printConfigWarnings is how `ctxloom run`, `ctxloom mcp`, and GetConfig-based
// commands surface the errors config.Load downgraded to warnings (CLAUDE.md
// fault tolerance) — without it a corrupted config.yaml silently launches an
// empty-context session.
func TestPrintConfigWarnings_EmitsPrefixedLinePerWarning(t *testing.T) {
	var buf bytes.Buffer

	printConfigWarnings(&buf, []config.Warning{
		{Kind: config.WarnKindParse, Text: "config.yaml is malformed: yaml: line 3: mapping values are not allowed"},
		{Kind: config.WarnKindValidate, Text: "profile \"dev\" failed schema validation"},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	assert.Len(t, lines, 2, "one line per warning")
	for _, line := range lines {
		assert.True(t, strings.HasPrefix(line, "ctxloom: warning: "),
			"each warning must carry the project-standard prefix, got %q", line)
	}
	assert.Contains(t, buf.String(), "config.yaml is malformed")
	assert.Contains(t, buf.String(), "failed schema validation")
}

func TestPrintConfigWarnings_NoWarningsIsSilent(t *testing.T) {
	var buf bytes.Buffer
	printConfigWarnings(&buf, nil)
	assert.Empty(t, buf.String())
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

func TestReportCompanions_PresentBinariesLogVersions(t *testing.T) {
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreProbe := config.SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return []byte(`{"name":"x","version":"v9.9.9"}`), nil
	})
	defer restoreProbe()
	var buf bytes.Buffer

	reportCompanions(&buf)

	assert.Contains(t, buf.String(), "ctxloom: companion taskloom v9.9.9")
	assert.Contains(t, buf.String(), "ctxloom: companion ltk v9.9.9")
}

func TestReportCompanions_MissingBinariesStaySilent(t *testing.T) {
	restoreLook := config.SetLookPathForTesting(func(string) (string, error) {
		return "", errors.New("not found")
	})
	defer restoreLook()
	var buf bytes.Buffer

	reportCompanions(&buf)

	assert.Empty(t, buf.String(), "install hints belong to the bundle resolvers, not the boot report")
}

func TestReportCompanions_ProbeFailureWarnsButContinues(t *testing.T) {
	restoreLook := config.SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreProbe := config.SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		return nil, errors.New("exec format error")
	})
	defer restoreProbe()
	var buf bytes.Buffer

	reportCompanions(&buf)

	assert.Contains(t, buf.String(), "ctxloom: warning: companion taskloom")
	assert.Contains(t, buf.String(), "ctxloom: warning: companion ltk")
}

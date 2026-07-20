package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
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

// An explicitly-requested-but-unsatisfiable container runtime is a fail-loudly
// finding (ClassIsolation) raised inside isolation.Prepare. The run path
// re-gates on it right after Prepare (isolation resolves AFTER the main startup
// gate), so it must abort with the distinct findings exit code (3) — before an
// UNSANDBOXED engine spawns — and stay a no-op under --degraded (the host
// degrade). This pins the class→choke mapping the run path relies on.
func TestFailOnFindings_ContainerIsolationDegradeIsFatalUnlessDegraded(t *testing.T) {
	const fixit = "install/build the agent image and start the container runtime (docker/podman), or pass --degraded (env CTXLOOM_DEGRADED=1) to run on the HOST without a sandbox"

	t.Run("strict mode aborts exit 3, names the lost boundary + escape hatch", func(t *testing.T) {
		resetStrictness(t)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		require.Error(t, err, "an explicitly-requested unsatisfiable container must abort before an unsandboxed launch")
		var exitErr *ExitError
		require.ErrorAs(t, err, &exitErr)
		assert.Equal(t, exitCodeFatalFindings, exitErr.Code, "must carry the distinct findings exit code (3)")

		out := buf.String()
		assert.Contains(t, out, "[isolation]", "the finding must be class-tagged for diagnosis")
		assert.Contains(t, out, "NOT sandboxed", "the abort must name the lost container boundary")
		assert.Contains(t, out, "--degraded", "the escape hatch must be surfaced")
	})

	t.Run("degraded mode never aborts — the host degrade is the accepted outcome", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)

		mark := strictness.Checkpoint()
		strictness.Fail(strictness.ClassIsolation, fixit,
			"container isolation was requested but could not start — running %q on the HOST without a container boundary (this session is NOT sandboxed): %v", "agent-a", "image absent")

		var buf bytes.Buffer
		err := failOnFindings(&buf, mark)

		assert.NoError(t, err, "--degraded downgrades the isolation finding to the plain host degrade")
		assert.Empty(t, buf.String(), "degraded mode renders no findings block")
	})
}

// The run path has TWO findings gates: gate 1 after config/sync/assembly
// (anchored at startupMark) and gate 2 after isolation.Prepare. Gate 2 must be
// anchored at a checkpoint captured IMMEDIATELY after gate 1 passes so the two
// windows tile: a finding recorded in the gap between them (trust gate, session
// accounting, hook work on the way to the launch) is still fatal. This pins the
// tiling contract run.go wires (postStartupMark); the old anchoring — a
// checkpoint taken just before Prepare — left the gap ungated.
func TestStartupGates_TileWithoutHole(t *testing.T) {
	resetStrictness(t)

	// Gate 1 passes clean.
	startupMark := strictness.Checkpoint()
	var gate1 bytes.Buffer
	require.NoError(t, failOnFindings(&gate1, startupMark), "gate 1 passes with nothing recorded")

	// run.go's anchoring: the gate-2 mark is captured immediately after gate 1.
	postStartupMark := strictness.Checkpoint()

	// A fault fires IN THE GAP — after gate 1, before isolation.Prepare.
	strictness.Fail(strictness.ClassTrust, "remove or restore the trust store", "trust store unreadable: boom")

	// The OLD anchoring (a checkpoint taken only when Prepare runs) misses it…
	lateMark := strictness.Checkpoint()
	var lateGate bytes.Buffer
	assert.NoError(t, failOnFindings(&lateGate, lateMark),
		"a gate anchored AFTER the gap cannot see the gap finding — the hole this test pins closed")

	// …the tiled anchoring catches it.
	var gate2 bytes.Buffer
	err := failOnFindings(&gate2, postStartupMark)
	require.Error(t, err, "gate 2 anchored at the post-gate-1 checkpoint must catch a gap finding")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, gate2.String(), "trust store unreadable")
}

// TestRecordExternalPluginIsolationDrop pins site 1: the external-plugin-binary
// path cannot be containerized or given a worktree. An EXPLICITLY-requested
// container is a lost sandbox boundary → a fatal ClassIsolation finding the run
// path's gate 2 aborts on (unless --degraded); a requested worktree degrades
// benignly to the live project dir and only warns (never a finding). Nothing
// requested → nothing recorded.
func TestRecordExternalPluginIsolationDrop(t *testing.T) {
	t.Run("strict: a requested container is one fatal isolation finding", func(t *testing.T) {
		resetStrictness(t)
		recordExternalPluginIsolationDrop(isolation.Axes{Runtime: isolation.RuntimeContainer}, "/opt/my-plugin")

		findings := strictness.All()
		require.Len(t, findings, 1, "a dropped container boundary on the external path is fatal")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "NOT sandboxed", "the finding must flag the lost boundary")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")
	})

	t.Run("strict: a requested worktree only warns — never a finding", func(t *testing.T) {
		resetStrictness(t)
		recordExternalPluginIsolationDrop(isolation.Axes{Workspace: isolation.WorkspaceWorktree}, "/opt/my-plugin")
		assert.Empty(t, strictness.All(),
			"a dropped worktree degrades to the live project dir — benign, only a lost CONTAINER boundary is fatal")
	})

	t.Run("strict: no isolation requested → nothing recorded", func(t *testing.T) {
		resetStrictness(t)
		recordExternalPluginIsolationDrop(isolation.Axes{}, "/opt/my-plugin")
		assert.Empty(t, strictness.All())
	})

	t.Run("degraded: a requested container records nothing", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		recordExternalPluginIsolationDrop(isolation.Axes{Runtime: isolation.RuntimeContainer}, "/opt/my-plugin")
		assert.Empty(t, strictness.All(), "--degraded downgrades the isolation finding to the plain host run")
	})
}

func TestLoadConfigOrFallback_Success(t *testing.T) {
	want := config.NewFixture(config.Fixture{AppPaths: []string{"/some/dir"}})
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
	assert.Equal(t, []string{".ctxloom"}, got.GetAppPaths(),
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
	resetStrictness(t) // printConfigWarnings records findings; keep them out of the shared collector
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

	// Each warning is ALSO recorded as a fatal finding so `ctxloom run`/`mcp`/`acp`
	// abort on a present-but-broken config (fail-loudly) instead of launching an
	// empty-context session — the whole point of surfacing them here.
	findings := strictness.All()
	require.Len(t, findings, 2, "each config warning records a fatal startup finding")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "a parse warning is config-class")
	assert.Equal(t, strictness.ClassConfig, findings[1].Class, "a validate warning is config-class")
	assert.NotEmpty(t, findings[0].FixIt, "the finding carries a fix-it hint")
	assert.Contains(t, findings[0].Message, "config.yaml is malformed", "the finding echoes the warning text")
}

// An unknown config key is the silent-no-op trap: ctxloom drops the key and
// launches with a context the user never asked for. In strict mode it must abort
// startup with a config-class finding that NAMES the key and carries a fix-it —
// a crash names itself, a no-op does not.
func TestPrintConfigWarnings_UnknownKeyIsFatalAndNamesTheKey(t *testing.T) {
	resetStrictness(t)
	var buf bytes.Buffer

	printConfigWarnings(&buf, []config.Warning{{
		Kind: config.WarnKindUnknownKey,
		Text: "unknown key `profiles.defaults` in /p/.ctxloom/config.yaml: ctxloom does not know it, so it is IGNORED — `profiles.defaults` was RETIRED",
	}})

	assert.Contains(t, buf.String(), "profiles.defaults", "the warning line names the offending key")

	findings := strictness.All()
	require.Len(t, findings, 1, "an unknown key is a fatal startup finding, not a silent drop")
	assert.Equal(t, strictness.ClassConfig, findings[0].Class, "an unknown key is config-class")
	assert.Contains(t, findings[0].Message, "profiles.defaults")
	assert.Contains(t, findings[0].FixIt, "config.yaml", "the finding tells the user where to make the edit")

	// The gate must actually refuse to launch.
	var gate bytes.Buffer
	err := failOnFindings(&gate, 0)
	require.Error(t, err, "strict startup aborts on the unknown-key finding")
	var exitErr *ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, gate.String(), "profiles.defaults")
	assert.Contains(t, gate.String(), "--degraded", "the abort names the escape hatch")
}

// --degraded / CTXLOOM_DEGRADED=1 is the established escape hatch: the same
// unknown key still WARNS (no diagnostic is ever lost) but records no finding, so
// startup proceeds. An unknown key must not be able to wedge a user out of their
// own tool.
func TestPrintConfigWarnings_UnknownKeyDegradesToWarning(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	var buf bytes.Buffer

	printConfigWarnings(&buf, []config.Warning{{
		Kind: config.WarnKindUnknownKey,
		Text: "unknown key `profilez` in /p/.ctxloom/config.yaml: ctxloom does not know it, so it is IGNORED",
	}})

	assert.Contains(t, buf.String(), "profilez", "degraded mode still prints the warning")
	assert.Empty(t, strictness.All(), "degraded mode records no fatal finding")
	assert.NoError(t, failOnFindings(&bytes.Buffer{}, 0), "degraded mode launches anyway")
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
	resetStrictness(t) // writeSyncSummary records a finding per failed item; keep them out of the shared collector
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

func TestWriteSyncSummary_InstalledAndErrorsBothPrinted(t *testing.T) {
	// Partial-success path: per CLAUDE.md, 9 of 10 succeeding is still success.
	// The user needs to see *both* what worked and what didn't.
	resetStrictness(t) // the failed item records a finding; keep it out of the shared collector
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

	// The single failed item is still recorded as a fatal sync finding even on the
	// partial-success path (installs succeeded, but the missing pinned item is fatal).
	findings := strictness.All()
	require.Len(t, findings, 1, "the failed item records a fatal sync finding despite partial success")
	assert.Equal(t, strictness.ClassSync, findings[0].Class)
	assert.Contains(t, findings[0].Message, "myorg/broken", "the finding names the failed item")
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

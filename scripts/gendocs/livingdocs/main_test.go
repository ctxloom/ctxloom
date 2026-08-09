package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"tests/acceptance/features/j000200_setup.feature":               "j000200-setup.md",
		"tests/acceptance/features/j000700_team_authoring.feature":      "j000700-team-authoring.md",
		"tests/acceptance/features/j001500_corporate_signed.feature":    "j001500-corporate-signed.md",
		"tests/acceptance/features/j000300_source_augmentation.feature": "j000300-source-augmentation.md",
		"tests/acceptance/features/j000800_onboarding.feature":          "j000800-onboarding.md",
	}
	for in, want := range cases {
		assert.Equal(t, want, slug(in), "slug(%q)", in)
	}
}

func writeCapture(t *testing.T, dir, name, scenario, status string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), `{"scenario":"`+scenario+`","steps":[{"text":"a step","keyword":"Given","status":"`+status+`"}]}`)
}

func TestRun_GeneratesPageForEachDocFeature(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"), "@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "j000800_onboarding.feature"), "@doc\nFeature: J000800\n\n  Scenario: S2\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "not_doc.feature"), "Feature: NotDoc\n\n  Scenario: S3\n    Given a step\n")

	writeCapture(t, captureDir, "s1.json", "S1", "passed")
	writeCapture(t, captureDir, "s2.json", "S2", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	assert.FileExists(t, filepath.Join(outDir, "j000200-setup.md"))
	assert.FileExists(t, filepath.Join(outDir, "j000800-onboarding.md"))
	assert.NoFileExists(t, filepath.Join(outDir, "not-doc.md"))
}

func TestRun_NarrationOptionalStillGeneratesPage(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	// No .doc.md companion written at all for this feature.
	writeFile(t, filepath.Join(featuresDir, "j000800_onboarding.feature"), "@doc\nFeature: J000800\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	content, err := os.ReadFile(filepath.Join(outDir, "j000800-onboarding.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Given a step")
}

func TestRun_RefusesWholeBatchOnAnyFailure(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"), "@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "j000800_onboarding.feature"), "@doc\nFeature: J000800\n\n  Scenario: S2\n    Given a step\n")

	writeCapture(t, captureDir, "s1.json", "S1", "passed")
	writeCapture(t, captureDir, "s2.json", "S2", "failed") // the poison pill

	err := run(featuresDir, captureDir, outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "S2")

	// Nothing gets written — not even the page for the feature that passed.
	assert.NoDirExists(t, outDir)
}

func TestRun_NoDocFeaturesErrors(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "not_doc.feature"), "Feature: NotDoc\n\n  Scenario: S1\n    Given a step\n")

	err := run(featuresDir, captureDir, outDir)
	assert.Error(t, err)
}

func TestRun_RefusesFeatureWithZeroCapturedScenarios(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	// Two scenarios, an entirely empty capture dir — the fail-OPEN bug: every
	// scenario would otherwise silently render "Not captured in this build"
	// while both the acceptance run and this generator still exit 0.
	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"),
		"@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n\n  Scenario: S2\n    Given another step\n")
	require.NoError(t, os.MkdirAll(captureDir, 0o755)) // exists, but empty

	err := run(featuresDir, captureDir, outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "J000200")
	assert.Contains(t, err.Error(), "ZERO captured scenarios")
	assert.NoDirExists(t, outDir)
}

func TestRun_TolerantOfSomeScenariosLegitimatelyUncaptured(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	// Mirrors j000400_multi_engine.feature's real shape: most scenarios captured,
	// one (e.g. @live, skipped for want of credentials) legitimately isn't —
	// that must still render fine, not trip the zero-captures guard.
	writeFile(t, filepath.Join(featuresDir, "j000400_multi_engine.feature"),
		"@doc\nFeature: J000400\n\n  Scenario: Captured\n    Given a step\n\n  @live\n  Scenario: Uncaptured live scenario\n    Given a live step\n")
	writeCapture(t, captureDir, "s1.json", "Captured", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	content, err := os.ReadFile(filepath.Join(outDir, "j000400-multi-engine.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Not captured in this build")
}

func TestRun_ClearsStalePagesFromPreviousRun(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(outDir, "stale-journey.md"), "leftover from a removed feature")
	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"), "@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	assert.NoFileExists(t, filepath.Join(outDir, "stale-journey.md"))
	assert.FileExists(t, filepath.Join(outDir, "j000200-setup.md"))
}

// TestRun_WarnsAboutOrphanedNarration pins that a doc:scenario marker matching
// no scenario is REPORTED. Before this the prose was parsed, never looked up,
// and dropped with no signal at all — the page generated clean and exit 0.
// Three such markers exist in this repo's own .doc.md companions today, which
// is how the silence was found.
func TestRun_WarnsAboutOrphanedNarration(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"), "@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "j000200_setup.doc.md"),
		"<!-- doc:scenario: S1 -->\nkept\n<!-- /doc:scenario -->\n"+
			"<!-- doc:scenario: Renamed Away -->\nORPHANED PROSE\n<!-- /doc:scenario -->\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	var warnings []string
	original := warnf
	warnf = func(format string, a ...any) { warnings = append(warnings, fmt.Sprintf(format, a...)) }
	t.Cleanup(func() { warnf = original })

	require.NoError(t, run(featuresDir, captureDir, outDir))

	require.Len(t, warnings, 1, "expected exactly one orphan warning, got %v", warnings)
	assert.Contains(t, warnings[0], "Renamed Away")
	assert.Contains(t, warnings[0], "will NOT appear")

	page, err := os.ReadFile(filepath.Join(outDir, "j000200-setup.md"))
	require.NoError(t, err)
	assert.Contains(t, string(page), "kept")
	assert.NotContains(t, string(page), "ORPHANED PROSE")
}

// TestRun_PropagatesAParseFailure characterizes run's parse-error arm: a @doc
// file that DiscoverDocFeatures accepted but ParseFeature cannot finish is
// returned verbatim, and nothing is written.
func TestRun_PropagatesAParseFailure(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "ok.feature"), "@doc\nFeature: OK\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, filepath.Join(root, "capture"), "s1.json", "S1", "passed")
	// DiscoverDocFeatures itself parses every file in the directory, so a
	// malformed sibling fails the discovery pass before any page is built.
	writeFile(t, filepath.Join(featuresDir, "broken.feature"), "@doc\n  Scenario: no feature line\n    Given a step\n")

	err := run(featuresDir, filepath.Join(root, "capture"), outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no 'Feature:' line found")
	assert.NoDirExists(t, outDir, "a parse failure must not touch the output directory")
}

// TestRun_ReportsAnUnusableOutputDirectory characterizes the disk-write arm:
// every feature validated clean, then the write itself fails.
func TestRun_ReportsAnUnusableOutputDirectory(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")

	writeFile(t, filepath.Join(featuresDir, "j000200_setup.feature"), "@doc\nFeature: J000200\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	// A regular FILE where the out dir's PARENT should be: the clear step
	// cannot even resolve the path.
	blocker := filepath.Join(root, "blocker")
	writeFile(t, blocker, "not a directory")
	outDir := filepath.Join(blocker, "out")

	err := run(featuresDir, captureDir, outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "clear ")
}

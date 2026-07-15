package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"tests/acceptance/features/j1_setup.feature":                "j1-setup.md",
		"tests/acceptance/features/j2_team_authoring.feature":       "j2-team-authoring.md",
		"tests/acceptance/features/j3_corporate_signed.feature":     "j3-corporate-signed.md",
		"tests/acceptance/features/j1b_source_augmentation.feature": "j1b-source-augmentation.md",
		"tests/acceptance/features/j4_onboarding.feature":           "j4-onboarding.md",
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

	writeFile(t, filepath.Join(featuresDir, "j1_setup.feature"), "@doc\nFeature: J1\n\n  Scenario: S1\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "j4_onboarding.feature"), "@doc\nFeature: J4\n\n  Scenario: S2\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "not_doc.feature"), "Feature: NotDoc\n\n  Scenario: S3\n    Given a step\n")

	writeCapture(t, captureDir, "s1.json", "S1", "passed")
	writeCapture(t, captureDir, "s2.json", "S2", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	assert.FileExists(t, filepath.Join(outDir, "j1-setup.md"))
	assert.FileExists(t, filepath.Join(outDir, "j4-onboarding.md"))
	assert.NoFileExists(t, filepath.Join(outDir, "not-doc.md"))
}

func TestRun_NarrationOptionalStillGeneratesPage(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	// No .doc.md companion written at all for this feature.
	writeFile(t, filepath.Join(featuresDir, "j4_onboarding.feature"), "@doc\nFeature: J4\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	content, err := os.ReadFile(filepath.Join(outDir, "j4-onboarding.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Given a step")
}

func TestRun_RefusesWholeBatchOnAnyFailure(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(featuresDir, "j1_setup.feature"), "@doc\nFeature: J1\n\n  Scenario: S1\n    Given a step\n")
	writeFile(t, filepath.Join(featuresDir, "j4_onboarding.feature"), "@doc\nFeature: J4\n\n  Scenario: S2\n    Given a step\n")

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
	writeFile(t, filepath.Join(featuresDir, "j1_setup.feature"),
		"@doc\nFeature: J1\n\n  Scenario: S1\n    Given a step\n\n  Scenario: S2\n    Given another step\n")
	require.NoError(t, os.MkdirAll(captureDir, 0o755)) // exists, but empty

	err := run(featuresDir, captureDir, outDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "J1")
	assert.Contains(t, err.Error(), "ZERO captured scenarios")
	assert.NoDirExists(t, outDir)
}

func TestRun_TolerantOfSomeScenariosLegitimatelyUncaptured(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	// Mirrors j5_multi_engine.feature's real shape: most scenarios captured,
	// one (e.g. @live, skipped for want of credentials) legitimately isn't —
	// that must still render fine, not trip the zero-captures guard.
	writeFile(t, filepath.Join(featuresDir, "j5_multi_engine.feature"),
		"@doc\nFeature: J5\n\n  Scenario: Captured\n    Given a step\n\n  @live\n  Scenario: Uncaptured live scenario\n    Given a live step\n")
	writeCapture(t, captureDir, "s1.json", "Captured", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	content, err := os.ReadFile(filepath.Join(outDir, "j5-multi-engine.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Not captured in this build")
}

func TestRun_ClearsStalePagesFromPreviousRun(t *testing.T) {
	root := t.TempDir()
	featuresDir := filepath.Join(root, "features")
	captureDir := filepath.Join(root, "capture")
	outDir := filepath.Join(root, "out")

	writeFile(t, filepath.Join(outDir, "stale-journey.md"), "leftover from a removed feature")
	writeFile(t, filepath.Join(featuresDir, "j1_setup.feature"), "@doc\nFeature: J1\n\n  Scenario: S1\n    Given a step\n")
	writeCapture(t, captureDir, "s1.json", "S1", "passed")

	require.NoError(t, run(featuresDir, captureDir, outDir))

	assert.NoFileExists(t, filepath.Join(outDir, "stale-journey.md"))
	assert.FileExists(t, filepath.Join(outDir, "j1-setup.md"))
}

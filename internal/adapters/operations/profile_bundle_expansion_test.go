package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// These integration tests guard against the regression where directory
// profiles (.ctxloom/profiles/<name>.yaml) that list bundles produced zero
// fragments — leaving apply_hooks with no SessionStart context-injection
// hook and assemble_context returning an empty string.
//
// They use real tempdirs (not afero.MemMapFs) because cfg.GetBundleDirs()
// and profiles.GetProfileDirs() check directory existence with os.Stat
// directly, so the bundles and profiles must live on the real filesystem
// for an honest end-to-end exercise of the resolution pipeline.

// writeBundleFixture writes a minimal .ctxloom layout:
//
//	<root>/.ctxloom/profiles/test.yaml         — directory profile listing two bundles
//	<root>/.ctxloom/content/bundles/test/alpha.yaml — bundle with two fragments
//	<root>/.ctxloom/content/bundles/test/beta.yaml  — bundle with two fragments
//
// The profile references "test/alpha" as a whole bundle (both fragments
// expanded) and "test/beta:fragments/two" as a cherry-picked fragment so
// the test asserts both expansion forms.
func writeBundleFixture(t *testing.T, root string) {
	t.Helper()

	profilesDir := bundletree.ProjectProfilesDir(t, filepath.Join(root, ".ctxloom"))
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	profileYAML := `description: "Test profile for bundle expansion"
bundles:
  - test/alpha
  - test/beta:fragments/two
`
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "test.yaml"), []byte(profileYAML), 0644))

	bundleDir := filepath.Join(authoredV1(filepath.Join(root, paths.AppDirName)), "test")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))

	alphaYAML := `version: "1.0.0"
fragments:
  a1:
    content: "ALPHA-ONE"
  a2:
    content: "ALPHA-TWO"
`
	betaYAML := `version: "1.0.0"
fragments:
  one:
    content: "BETA-ONE"
  two:
    content: "BETA-TWO"
`
	bundletree.WriteOS(t, bundleDir, "alpha", alphaYAML)
	bundletree.WriteOS(t, bundleDir, "beta", betaYAML)
}

// fixtureConfig builds a *config.Config rooted at the given tempdir with the
// directory profile "test" bound as the default agent's sole profile (the
// default set a bare regenerate reads). No inline profiles are declared, which
// is the case that previously failed.
func fixtureConfig(root string) *config.Config {
	return config.NewFixture(config.Fixture{
		AppPaths:     []string{filepath.Join(root, ".ctxloom")},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"test"}}},
	})
}

// TestMaterialize_DirectoryProfileWithBundles_WritesContextAndSessionStartHook
// pins the bundle-expansion fix at rest: when the only profile lives in
// .ctxloom/profiles/ and lists bundles (not inline fragment refs), the
// materialized context file carries every fragment, and the settings register
// ctxloom's SessionStart hook — which names no context.
func TestMaterialize_DirectoryProfileWithBundles_WritesContextAndSessionStartHook(t *testing.T) {
	ownRecordsDir(t)
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)

	cfg := fixtureConfig(tmpDir)
	mockLoader := func() (*config.Config, error) { return cfg, nil }

	result, err := Materialize(context.Background(), engines.Registry(), loaded(t, mockLoader), MaterializeRequest{
		Engines: []string{"claude-code"},
		Target:  tmpDir,
	})
	require.NoError(t, err)
	assert.Equal(t, MaterializeApplied, result.Status, "%v", result.Errors)

	// The context file contains content from all three referenced fragments
	// (both alphas, only beta:two).
	contextPath := filepath.Join(tmpDir, claude.ContextFileName)
	contextBytes, err := os.ReadFile(contextPath)
	require.NoError(t, err, "context file should be written at %s", contextPath)
	contextStr := string(contextBytes)

	assert.Contains(t, contextStr, "ALPHA-ONE", "whole-bundle ref should pull every fragment")
	assert.Contains(t, contextStr, "ALPHA-TWO", "whole-bundle ref should pull every fragment")
	assert.Contains(t, contextStr, "BETA-TWO", "cherry-picked fragment should be present")
	assert.NotContains(t, contextStr, "BETA-ONE", "non-cherry-picked fragment must be excluded")

	// Settings file gained ctxloom's SessionStart hook, and that hook names
	// no context: no fragment's text rides the settings file. We assert by substring rather than parsing JSON
	// because the exact key path differs by backend.
	settingsBytes, err := os.ReadFile(filepath.Join(tmpDir, ".claude", "settings.json"))
	require.NoError(t, err)
	settingsStr := string(settingsBytes)
	assert.Contains(t, settingsStr, "SessionStart", "SessionStart hook should be registered")
	assert.Contains(t, settingsStr, `"session-start"`, "hook must invoke ctxloom hook session-start")
	assert.NotContains(t, settingsStr, "ALPHA-ONE", "no fragment text may ride the settings file")
}

// TestAssembleContext_DirectoryProfileWithBundles_ReturnsAllFragments pins
// the assemble_context fix: a directory profile that only lists bundles
// must resolve to every fragment in those bundles (whole-bundle refs) and
// to the specified fragment for cherry-picked refs.
func TestAssembleContext_DirectoryProfileWithBundles_ReturnsAllFragments(t *testing.T) {
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)

	cfg := fixtureConfig(tmpDir)

	result, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Profile: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, []string{"test"}, result.Profiles)

	// All three expected fragments resolve; the suppressed one does not.
	// FragmentsLoaded echoes the canonical refs we asked the loader for, in
	// the "<canonical-bundle>#fragments/<name>" form emitted by
	// ExpandBundleRefs — local bundles carry the ctxloom+local identity.
	assert.ElementsMatch(t,
		[]string{
			"ctxloom+local:test/alpha#fragments/a1",
			"ctxloom+local:test/alpha#fragments/a2",
			"ctxloom+local:test/beta#fragments/two",
		},
		result.FragmentsLoaded,
		"FragmentsLoaded must include every expanded fragment")

	assert.Contains(t, result.Context, "ALPHA-ONE")
	assert.Contains(t, result.Context, "ALPHA-TWO")
	assert.Contains(t, result.Context, "BETA-TWO")
	assert.NotContains(t, result.Context, "BETA-ONE")
}

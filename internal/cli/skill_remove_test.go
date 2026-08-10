// Tests for skill_cmd.go's `skill remove` — mirrors agent_test.go's
// TestRunAgentRemove_BareReportsAndDestroysNothing/
// TestRunAgentRemove_YesRemovesAndReports pair.
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// seedRemovableSkill scaffolds a directory-form bundle "b" and a REAL, valid
// skill "greet" inside it via operations.CreateSkill — the same production
// path `skill create` uses — so the fixture passes the same frontmatter
// validation `skill list`/`skill show` apply, unlike the signing journey's
// hand-built minimal fixture (push_directory_form_test.go's
// writeDirFormBundle), which is deliberately NOT a well-formed package. It
// returns the skill's on-disk directory.
func seedRemovableSkill(t *testing.T, cfg *config.Config) string {
	t.Helper()
	bundleDir := filepath.Join(paths.LocalBundlesPath(cfg.GetAppPaths()[0]), "b")
	require.NoError(t, os.MkdirAll(bundleDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "bundle.yaml"), []byte("version: \"1.0\"\n"), 0o644))

	res, err := operations.CreateSkill(t.Context(), cfg, operations.CreateSkillRequest{
		Bundle: "b", Name: "greet", Description: "Say hello.",
	})
	require.NoError(t, err)
	return res.Dir
}

func TestRunSkillRemove_BareReportsAndDestroysNothing(t *testing.T) {
	agentProject(t, "version: 6\n")
	cfg, err := GetConfig()
	require.NoError(t, err)
	greetDir := seedRemovableSkill(t, cfg)

	skillRemoveYes = false
	cmd, out := textCmd()
	require.NoError(t, runSkillRemove(cmd, []string{"b#skills/greet"}))
	assert.Contains(t, out.String(), "Nothing was removed")
	assert.Contains(t, out.String(), "--yes")
	assert.DirExists(t, greetDir, "the bare (no --yes) path must leave the skill on disk")
}

// TestRunSkillRemove_YesRemovesAndReports pins the apply side, paired with
// the bare-path test above.
func TestRunSkillRemove_YesRemovesAndReports(t *testing.T) {
	agentProject(t, "version: 6\n")
	cfg, err := GetConfig()
	require.NoError(t, err)
	greetDir := seedRemovableSkill(t, cfg)

	skillRemoveYes = true
	t.Cleanup(func() { skillRemoveYes = false })
	cmd, out := textCmd()
	require.NoError(t, runSkillRemove(cmd, []string{"b#skills/greet"}))
	assert.Contains(t, out.String(), `Removed skill "greet" from bundle "b"`)

	_, statErr := os.Stat(greetDir)
	assert.True(t, os.IsNotExist(statErr), "--yes must actually remove the skill's directory tree")
}

func TestRunSkillRemove_UnknownNameErrors(t *testing.T) {
	agentProject(t, "version: 6\n")
	cfg, err := GetConfig()
	require.NoError(t, err)
	seedRemovableSkill(t, cfg)

	skillRemoveYes = false
	cmd, _ := textCmd()
	err = runSkillRemove(cmd, []string{"b#skills/nope"})
	require.Error(t, err)
}

func TestRunSkillRemove_InvalidRefIsUsageError(t *testing.T) {
	agentProject(t, "version: 6\n")
	cmd, _ := textCmd()
	err := runSkillRemove(cmd, []string{"not-a-valid-ref"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bundle#skills/name")
}

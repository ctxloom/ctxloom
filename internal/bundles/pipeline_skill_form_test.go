package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Form selection for a skill package is process-stage policy, like it is for a
// command: the read reports every body the package carries, and the pipeline
// picks one. What a skill delivers is the package as an engine receives it —
// the selected body at SKILL.md, the other body nowhere — so an engine writer
// downstream never sees two bodies and never has to know a form exists.

var distilledSkillMD = []byte("---\nname: humanize\ndescription: Does a thing.\n---\n\nShort body.\n")

// writeTwoBodySkillBundle is writeSkillBundle plus a distilled body beside the
// descriptor. Returns the fixture's raw-body bytes.
func writeTwoBodySkillBundle(t *testing.T, fsys afero.Fs, bundlesDir string) map[string][]byte {
	t.Helper()
	files := writeSkillBundle(t, fsys, bundlesDir, "skill-bundle", "humanize", true)
	dir := skillFixtureDir(t, fsys, bundlesDir)
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.distilled.md", distilledSkillMD, 0644))
	return files
}

// skillFixtureDir locates the package writeSkillBundle wrote, by asking the
// loader rather than re-deriving the layout root.
func skillFixtureDir(t *testing.T, fsys afero.Fs, bundlesDir string) string {
	t.Helper()
	loader := NewLoader(NewProjectReader(fsys, []string{bundlesDir}))
	read, err := loader.Read("skill-bundle")
	require.NoError(t, err)
	bundleDir, err := read.Bundle.FSDir()
	require.NoError(t, err)
	dir, err := ResolveSkillDir(bundleDir, "humanize", read.Bundle.Skills["humanize"])
	require.NoError(t, err)
	return dir
}

func filesByPath(ls *LoadedSkill) map[string][]byte {
	out := make(map[string][]byte, len(ls.Files))
	for _, f := range ls.Files {
		out[f.RelPath] = f.Content
	}
	return out
}

func TestSkillsFromBundleRef_PreferDistilledMaterializesTheDistilledBodyAtTheDescriptor(t *testing.T) {
	fsys := afero.NewMemMapFs()
	files := writeTwoBodySkillBundle(t, fsys, "/bundles")
	loader := NewLoader(NewProjectReader(fsys, []string{"/bundles"}))

	got := ungated(loader, true).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)
	byPath := filesByPath(got[0])
	assert.Equal(t, distilledSkillMD, byPath["SKILL.md"], "the distilled body lands at SKILL.md")
	assert.NotContains(t, byPath, "SKILL.distilled.md", "the unselected body is not delivered under its own name")
	assert.Equal(t, files["scripts/run.sh"], byPath["scripts/run.sh"])
	assert.Equal(t, files["assets/logo.png"], byPath["assets/logo.png"])
	assert.Len(t, byPath, 3)
}

func TestSkillsFromBundleRef_PreferRawMaterializesTheRawBodyOnly(t *testing.T) {
	fsys := afero.NewMemMapFs()
	files := writeTwoBodySkillBundle(t, fsys, "/bundles")
	loader := NewLoader(NewProjectReader(fsys, []string{"/bundles"}))

	got := ungated(loader, false).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)
	byPath := filesByPath(got[0])
	assert.Equal(t, files["SKILL.md"], byPath["SKILL.md"])
	assert.NotContains(t, byPath, "SKILL.distilled.md", "the distilled body is not delivered when raw is selected")
	assert.Len(t, byPath, 3)
}

// The preference only PREFERS: a package with a single body serves it whatever
// the pipeline would rather have, exactly as a command without a distillation
// serves raw.
func TestSkillsFromBundleRef_PreferDistilledOnAOneBodyPackageServesRaw(t *testing.T) {
	fsys := afero.NewMemMapFs()
	files := writeSkillBundle(t, fsys, "/bundles", "skill-bundle", "humanize", true)
	loader := NewLoader(NewProjectReader(fsys, []string{"/bundles"}))

	got := ungated(loader, true).SkillsFromBundleRef("skill-bundle")
	require.Len(t, got, 1)
	byPath := filesByPath(got[0])
	assert.Equal(t, files["SKILL.md"], byPath["SKILL.md"])
	assert.Len(t, byPath, 3)
}

func TestGetSkill_AppliesTheSameBodySelection(t *testing.T) {
	fsys := afero.NewMemMapFs()
	writeTwoBodySkillBundle(t, fsys, "/bundles")
	loader := NewLoader(NewProjectReader(fsys, []string{"/bundles"}))

	ls, err := ungated(loader, true).GetSkill("skill-bundle#skills/humanize")
	require.NoError(t, err)
	byPath := filesByPath(ls)
	assert.Equal(t, distilledSkillMD, byPath["SKILL.md"])
	assert.NotContains(t, byPath, "SKILL.distilled.md")
}

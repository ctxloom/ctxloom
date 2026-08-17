// Profile skill-curation tests (Part B6a) verify the opt-in exposure
// semantics of LoadSkillExports: a profile with a non-empty skills: list
// exports EXACTLY those (force-enabled), suppressing the uncurated
// bundle-wide auto-export; an uncurated profile falls back to every
// profile-referenced bundle's skills (config.ResolveBundleSkills), each still
// gated by its own per-engine enablement flag. Mirrors
// command_curation_test.go, but a skill has no inline content the way a
// command does (BundleSkill carries no `content:` — see skill.go), so these
// tests build REAL on-disk directory-form bundles/profiles (like
// operations.TestMaterializeProfile_WritesSkills) rather than seeding bare
// in-memory bundle structs.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// skillCurationFixture writes a directory-form "skill-bundle" bundle
// shipping two skills — "shown" and "hidden" — under appDir's content tree.
// "hidden" additionally opts out of the claude-code export at the bundle
// level, so a curation test can prove force-enable overrides it.
func skillCurationFixture(t *testing.T, appDir string) {
	t.Helper()
	bundlesDir := paths.LocalBundlesPath(appDir)
	bundleDir := filepath.Join(bundlesDir, "skill-bundle")

	for _, name := range []string{"shown", "hidden"} {
		dir := filepath.Join(bundleDir, "skills", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+name+"\ndescription: The "+name+" skill.\n---\n\nBody.\n"), 0o644))
	}

	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "bundle.yaml"), []byte(
		"version: \"1.0\"\nskills:\n"+
			"  shown: {}\n"+
			"  hidden:\n    llm:\n      claude-code:\n        enabled: false\n"), 0o644))
}

// writeSkillProfile writes a directory profile YAML referencing skill-bundle,
// with an optional skills: curation body appended verbatim.
func writeSkillProfile(t *testing.T, appDir, name, curationYAML string) {
	t.Helper()
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	body := "name: " + name + "\nbundles:\n  - skill-bundle\n" + curationYAML
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, name+".yaml"), []byte(body), 0o644))
}

// skillItemNames returns the bare Item names of a LoadSkillExports result, for
// set-equality assertions.
func skillItemNames(skills []*bundles.LoadedSkill) []string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Item)
	}
	return names
}

// TestLoadSkillExports_CuratedSetExportsExactlyThoseAndSuppressesUncurated
// proves a profile's non-empty skills: list exports ONLY the curated skill;
// the bundle's OTHER skill ("hidden") — which the bundle itself does NOT
// disable globally — is suppressed for this profile because curation
// replaces the auto-export, not because of any per-engine opt-out.
func TestLoadSkillExports_CuratedSetExportsExactlyThoseAndSuppressesUncurated(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	skillCurationFixture(t, appDir)
	writeSkillProfile(t, appDir, "curated", "skills:\n  - skill-bundle#skills/shown\n")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	skills := LoadSkillExports(cfg, []string{"curated"})

	assert.ElementsMatch(t, []string{"shown"}, skillItemNames(skills),
		"only the profile-curated skill exports; the bundle's other skill ('hidden') is suppressed by curation")
}

// TestLoadSkillExports_UncuratedProfileExportsAllBundleSkills proves an
// uncurated profile (no skills: list) falls back to every skill its
// referenced bundles ship — both "shown" and "hidden" — the bundle-wide
// auto-export config.ResolveBundleSkills implements.
func TestLoadSkillExports_UncuratedProfileExportsAllBundleSkills(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	skillCurationFixture(t, appDir)
	writeSkillProfile(t, appDir, "uncurated", "")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	skills := LoadSkillExports(cfg, []string{"uncurated"})

	assert.ElementsMatch(t, []string{"shown", "hidden"}, skillItemNames(skills),
		"an uncurated profile exports every skill its bundles ship")

	// Per-engine opt-out is still respected on the UNCURATED path: "hidden"
	// disabled claude-code at the bundle level must resolve disabled for
	// claude-code, while "shown" (no opt-out) resolves enabled.
	ex := claudeSkillExports(skills)
	byName := map[string]bool{}
	for _, e := range ex {
		byName[e.Name] = e.Enabled
	}
	require.Contains(t, byName, "shown")
	require.Contains(t, byName, "hidden")
	assert.True(t, byName["shown"], "a skill with no opt-out exports enabled")
	assert.False(t, byName["hidden"], "the bundle's own claude-code opt-out is honored when uncurated")
}

// TestLoadSkillExports_CuratedForceEnablesBundleOptOut proves a profile that
// explicitly curates a skill the bundle opted OUT of the claude-code export
// still exports it enabled — the profile's explicit curation overrides the
// bundle's per-engine opt-out, mirroring
// TestLoadCommandExports_CuratedForceEnablesOptOut for commands.
func TestLoadSkillExports_CuratedForceEnablesBundleOptOut(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	skillCurationFixture(t, appDir)
	writeSkillProfile(t, appDir, "curated-hidden", "skills:\n  - skill-bundle#skills/hidden\n")

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	skills := LoadSkillExports(cfg, []string{"curated-hidden"})
	require.ElementsMatch(t, []string{"hidden"}, skillItemNames(skills))

	ex := claudeSkillExports(skills)
	require.Len(t, ex, 1)
	assert.True(t, ex[0].Enabled, "curating a skill force-enables it even though the bundle opted it out of claude-code")
}

// Profile-scope tests for the assembled package: the skills and commands a
// run carries come from the bundles of the profiles it SELECTED (or the
// configured defaults when it selected none), and a profile that cannot be
// resolved is reported rather than silently contributing nothing. Each goes
// through AssemblePackage, so composite.Assemble's real selection decides.
package operations

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"
)

// scopeFixture writes one directory profile per entry of profileBundles
// (profile name → the single bundle it references) and returns appDir and
// its content tree for the caller's bundles.
func scopeFixture(t *testing.T, profileBundles map[string]string) (appDir, bundlesDir string) {
	t.Helper()
	testsupport.Isolate(t)
	appDir = filepath.Join(t.TempDir(), paths.AppDirName)
	profilesDir := bundletree.ProjectProfilesDir(t, appDir)
	bundlesDir = paths.LocalBundlesPathFor(appDir, paths.LayoutV2)
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	for profile, bundle := range profileBundles {
		require.NoError(t, os.WriteFile(filepath.Join(profilesDir, profile+".yaml"),
			[]byte("bundles:\n  - "+bundle+"\n"), 0o644))
	}
	return appDir, bundlesDir
}

func defaultsTo(appDir string, profiles ...string) *config.Config {
	return gatedFixture(config.Fixture{
		AppPaths:     []string{appDir},
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: profiles}},
	})
}

// A directory profile's bundle-shipped Agent Skill package reaches the
// package whole: its frontmatter, its authored per-engine block, and its
// script with the exec bit intact.
func TestAssemblePackage_SkillFromDirectoryProfile(t *testing.T) {
	appDir, bundlesDir := scopeFixture(t, map[string]string{"dev": "skill-bundle"})
	skillDir := filepath.Join(bundlesDir, "skill-bundle", "skills", "humanize")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: humanize\ndescription: Removes AI writing tells.\n---\n\nInstructions.\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"),
		[]byte("#!/bin/sh\necho hi\n"), 0o755))
	bundletree.WriteOS(t, bundlesDir, "skill-bundle", "version: \"1.0\"\nskills:\n  humanize:\n    exports:\n      claude-code:\n        enabled: true\n")

	got := skillsOf(t, defaultsTo(appDir, "dev"), nil)
	require.Len(t, got, 1, "the profile's bundle-shipped skill is assembled")
	assert.Equal(t, "humanize", got[0].Frontmatter.Name)
	assert.Equal(t, "Removes AI writing tells.", got[0].Frontmatter.Description)
	assert.JSONEq(t, `{"enabled":true}`, string(got[0].Exports["claude-code"]), "the authored block is carried through")

	modes := map[string]uint32{}
	for _, f := range got[0].Files {
		modes[f.RelPath] = f.Mode
	}
	require.Contains(t, modes, "scripts/run.sh", "the script is part of the assembled skill's file set")
	assert.Equal(t, uint32(0o755), modes["scripts/run.sh"], "the exec bit survives assembly")
}

// Skills follow the SELECTED profile: an explicit selection carries that
// profile's bundles' skills and not the default's, and no selection carries
// the default's.
func TestAssemblePackage_SkillsFollowTheSelectedProfile(t *testing.T) {
	appDir, bundlesDir := scopeFixture(t, map[string]string{"default": "default-bundle", "other": "other-bundle"})
	for bundle, skill := range map[string]string{"default-bundle": "default-skill", "other-bundle": "other-skill"} {
		dir := filepath.Join(bundlesDir, bundle, "skills", skill)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+skill+"\ndescription: A skill.\n---\n\nBody.\n"), 0o644))
		bundletree.WriteOS(t, bundlesDir, bundle, "version: \"1.0\"\nskills:\n  "+skill+": {}\n")
	}
	cfg := defaultsTo(appDir, "default")

	assert.Equal(t, []string{"default-skill"}, skillItemNames(skillsOf(t, cfg, nil)))
	assert.Equal(t, []string{"other-skill"}, skillItemNames(skillsOf(t, cfg, []string{"other"})),
		"selecting other must not pull the default profile's skills")
}

// Commands follow the SELECTED profile the same way: `run -p finder` must not
// leak the default profile's commands into finder's session.
func TestAssemblePackage_CommandsFollowTheSelectedProfile(t *testing.T) {
	appDir, bundlesDir := scopeFixture(t, map[string]string{"developer": "dev-bundle", "finder": "finder-bundle"})
	bundletree.WriteOS(t, bundlesDir, "dev-bundle", "version: \"1.0\"\ncommands:\n  dev-skill:\n    description: d\n    content: c\n")
	bundletree.WriteOS(t, bundlesDir, "finder-bundle", "version: \"1.0\"\ncommands:\n  finder-skill:\n    description: f\n    content: c\n")
	cfg := defaultsTo(appDir, "developer")

	assert.Equal(t, []string{"finder-skill"}, bundlePromptItems(commandsOf(t, cfg, []string{"finder"})),
		"selecting finder must not pull the default profile's commands")
	assert.Equal(t, []string{"dev-skill"}, bundlePromptItems(commandsOf(t, cfg, nil)),
		"no selection carries the configured default's commands")
}

// A default profile that cannot be resolved is not "nothing configured": it
// is "we could not work out what to deliver", so it is recorded as a ref
// finding naming the profile and the rest still assembles. Named explicitly,
// the same profile is the user's own ask, and its failure is the error.
func TestAssemblePackage_UnresolvableProfileIsReported(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)
	appDir, bundlesDir := scopeFixture(t, map[string]string{"real": "real-bundle"})
	bundletree.WriteOS(t, bundlesDir, "real-bundle", "version: \"1.0\"\ncommands:\n  real-cmd:\n    content: c\n")
	cfg := defaultsTo(appDir, "nonexistent", "real")

	mark := strictness.Checkpoint()
	assert.Equal(t, []string{"real-cmd"}, bundlePromptItems(commandsOf(t, cfg, nil)),
		"the resolvable default still assembles")
	var named bool
	for _, f := range strictness.Since(mark) {
		if f.Kind == report.KindRef && strings.Contains(f.Text, "nonexistent") {
			named = true
		}
	}
	assert.True(t, named, "an unresolvable default profile must record a ref finding naming it, not vanish")

	_, err := AssemblePackage(context.Background(), cfg, PackageRequest{Profiles: []string{"nonexistent"}})
	require.Error(t, err, "an explicitly selected profile that cannot be resolved fails the assembly")
	assert.Contains(t, err.Error(), "nonexistent")
}

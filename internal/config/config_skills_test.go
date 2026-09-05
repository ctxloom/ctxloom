package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestConfig_ResolveBundleSkills_FromDirectoryProfile is the config-level
// end-to-end proof of the skill/command split's UNCURATED loadout path
// (skill-command-split.plan.md §3.2): a directory profile's `bundles:` entry
// pulls in a bundle-shipped Agent Skill package, mirroring
// TestConfig_ResolveBundleMCPServers_InheritedBundle's fixture shape for
// skills instead of MCP servers.
func TestConfig_ResolveBundleSkills_FromDirectoryProfile(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))
	skillDir := filepath.Join(bundlesDir, "skill-bundle", "skills", "humanize")
	require.NoError(t, os.MkdirAll(filepath.Join(skillDir, "scripts"), 0755))

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"),
		[]byte("name: dev\nbundles:\n  - skill-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "skill-bundle", "bundle.yaml"),
		[]byte("version: \"1.0\"\nskills:\n  humanize:\n    llm:\n      claude-code:\n        enabled: true\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: humanize\ndescription: Removes AI writing tells.\n---\n\nInstructions.\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"),
		[]byte("#!/bin/sh\necho hi\n"), 0755))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"dev"}}},
		appPaths: []string{appDir},
	}

	got := cfg.ResolveBundleSkills(nil)
	require.Len(t, got, 1, "the profile's bundle-shipped skill resolves")
	assert.Equal(t, "humanize", got[0].Frontmatter.Name)
	assert.Equal(t, "Removes AI writing tells.", got[0].Frontmatter.Description)
	assert.True(t, got[0].LLM.ClaudeCode.IsEnabled())

	var sawScript bool
	for _, f := range got[0].Files {
		if f.RelPath == "scripts/run.sh" {
			sawScript = true
			assert.Equal(t, uint32(0755), f.Mode, "the exec bit survives config-level resolution")
		}
	}
	assert.True(t, sawScript, "scripts/run.sh is part of the resolved skill's file set")
}

// TestConfig_ResolveBundleSkills_ScopedToSelectedProfile proves an explicit
// profile selection scopes the resolved skill set to THAT profile's bundles,
// distinct from the configured default — mirroring
// TestConfig_ResolveBundle_ScopesToSelectedProfile for commands/MCP.
func TestConfig_ResolveBundleSkills_ScopedToSelectedProfile(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	profilesDir := filepath.Join(appDir, "profiles")
	bundlesDir := paths.LocalBundlesPath(appDir)
	require.NoError(t, os.MkdirAll(profilesDir, 0755))

	writeSkill := func(bundleName, skillName string) {
		dir := filepath.Join(bundlesDir, bundleName, "skills", skillName)
		require.NoError(t, os.MkdirAll(dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, bundleName, "bundle.yaml"),
			[]byte("version: \"1.0\"\nskills:\n  "+skillName+":\n"), 0644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"),
			[]byte("---\nname: "+skillName+"\ndescription: A skill.\n---\n\nBody.\n"), 0644))
	}
	writeSkill("default-bundle", "default-skill")
	writeSkill("other-bundle", "other-skill")

	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "default.yaml"),
		[]byte("name: default\nbundles:\n  - default-bundle\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "other.yaml"),
		[]byte("name: other\nbundles:\n  - other-bundle\n"), 0644))

	cfg := &Config{
		defaultAgent: "default", agents: map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
		appPaths: []string{appDir},
	}

	defaultResult := cfg.ResolveBundleSkills(nil)
	require.Len(t, defaultResult, 1)
	assert.Equal(t, "default-skill", defaultResult[0].Frontmatter.Name)

	otherResult := cfg.ResolveBundleSkills([]string{"other"})
	require.Len(t, otherResult, 1)
	assert.Equal(t, "other-skill", otherResult[0].Frontmatter.Name)
}

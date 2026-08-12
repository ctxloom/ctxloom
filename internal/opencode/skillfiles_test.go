package opencode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/ledger"
)

// TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes proves an enabled
// skill materializes at .opencode/skill/<name>/SKILL.md plus its sibling
// files, with each file's mode (the exec bit on scripts/ in particular)
// preserved.
func TestWriteSkillFiles_EnabledSkillLandsAtPathWithModes(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files: []agent.PackageFile{
			{RelPath: "SKILL.md", Content: []byte("---\nname: humanize\ndescription: d\n---\n\nBody\n"), Mode: 0644},
			{RelPath: "scripts/run.sh", Content: []byte("#!/bin/sh\n"), Mode: 0755},
			{RelPath: "assets/data.txt", Content: []byte("data"), Mode: 0644},
		},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))

	base := filepath.Join(dir, ".opencode", "skill", "humanize")
	content, err := afero.ReadFile(fs, filepath.Join(base, "SKILL.md"))
	require.NoError(t, err)
	assert.Contains(t, string(content), "Body")

	info, err := fs.Stat(filepath.Join(base, "scripts", "run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), info.Mode().Perm())

	info, err = fs.Stat(filepath.Join(base, "assets", "data.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), info.Mode().Perm())

	manifest, err := afero.ReadFile(fs, filepath.Join(dir, ".opencode", "skill", ledger.Name))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), "humanize/SKILL.md")
	assert.Contains(t, string(manifest), "humanize/scripts/run.sh")
}

// TestWriteSkillFiles_DisabledSkillNotWritten proves a disabled skill is
// never written to disk and never manifest-tracked.
func TestWriteSkillFiles_DisabledSkillNotWritten(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	skills := []agent.SkillExport{{
		Name:    "off",
		Enabled: false,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("should not appear")}},
	}}

	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))

	exists, _ := afero.Exists(fs, filepath.Join(dir, ".opencode", "skill", "off", "SKILL.md"))
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, filepath.Join(dir, ".opencode", "skill", ledger.Name))
	assert.False(t, exists, "nothing written means no manifest at all")
}

// TestWriteSkillFiles_CleanupPreservesForeignSkill proves re-materializing
// with fewer skills reverts only the manifest-tracked set — a foreign,
// user-authored skill directory in .opencode/skill/ survives.
func TestWriteSkillFiles_CleanupPreservesForeignSkill(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	foreign := filepath.Join(dir, ".opencode", "skill", "my-own-skill", "SKILL.md")
	require.NoError(t, afero.WriteFile(fs, foreign, []byte("hand authored"), 0644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("managed"), Mode: 0644}},
	}}
	require.NoError(t, WriteSkillFiles(dir, skills, agent.WithCommandFS(fs)))
	exists, _ := afero.Exists(fs, filepath.Join(dir, ".opencode", "skill", "humanize", "SKILL.md"))
	require.True(t, exists)

	// Cleanup: re-materialize with no skills.
	require.NoError(t, WriteSkillFiles(dir, nil, agent.WithCommandFS(fs)))

	exists, _ = afero.Exists(fs, filepath.Join(dir, ".opencode", "skill", "humanize", "SKILL.md"))
	assert.False(t, exists)
	exists, _ = afero.Exists(fs, foreign)
	assert.True(t, exists, "a foreign skill directory must survive cleanup")
	content, err := afero.ReadFile(fs, foreign)
	require.NoError(t, err)
	assert.Equal(t, "hand authored", string(content))
}

// TestReconcileSkillsSurface_RegistersSkillsPathPreservingForeignKeys proves
// that materializing an enabled skill ALSO registers opencode's skills dir
// in opencode.json's `skills.paths` (belt-and-suspenders explicitness — see
// managedConfig.skillPaths for why this isn't load-bearing for the default
// dir), preserving every foreign top-level key and any pre-existing
// `skills.urls` entry.
func TestReconcileSkillsSurface_RegistersSkillsPathPreservingForeignKeys(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"
	existing := `{
  "$schema": "https://opencode.ai/config.json",
  "theme": "tokyonight",
  "skills": { "urls": ["https://example.com/.well-known/skills/"] }
}`
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, ConfigFileName), []byte(existing), 0o644))

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("body"), Mode: 0644}},
	}}
	require.NoError(t, reconcileSkillsSurface(fs, dir, skills))

	got := readJSON(t, fs, filepath.Join(dir, ConfigFileName))
	assert.Equal(t, "tokyonight", got["theme"], "foreign top-level key preserved")
	skillsObj, ok := got["skills"].(map[string]any)
	require.True(t, ok, "skills object present")
	assert.Equal(t, []any{"https://example.com/.well-known/skills/"}, skillsObj["urls"], "foreign skills.urls preserved")
	paths, ok := skillsObj["paths"].([]any)
	require.True(t, ok, "skills.paths present")
	assert.Contains(t, paths, opencodeSkillDir)
}

// TestReconcileSkillsSurface_NoEnabledSkillsUnregistersPath proves that
// reconciling with no enabled skills (the cleanup call) removes ctxloom's
// skills.paths entry while leaving a foreign entry in place.
func TestReconcileSkillsSurface_NoEnabledSkillsUnregistersPath(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := "/proj"

	skills := []agent.SkillExport{{
		Name:    "humanize",
		Enabled: true,
		Files:   []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("body"), Mode: 0644}},
	}}
	require.NoError(t, reconcileSkillsSurface(fs, dir, skills))
	got := readJSON(t, fs, filepath.Join(dir, ConfigFileName))
	skillsObj := got["skills"].(map[string]any)
	assert.Contains(t, skillsObj["paths"], opencodeSkillDir)

	// Plant a foreign path entry alongside ours before reverting.
	cfg, err := loadOpencodeConfig(fs, filepath.Join(dir, ConfigFileName))
	require.NoError(t, err)
	_, err = applyManaged(cfg, managedConfig{skillPaths: []string{"/foreign/skills"}})
	require.NoError(t, err)
	require.NoError(t, saveOpencodeConfig(fs, filepath.Join(dir, ConfigFileName), cfg))

	// Revert: reconcile with no skills at all.
	require.NoError(t, reconcileSkillsSurface(fs, dir, nil))

	got = readJSON(t, fs, filepath.Join(dir, ConfigFileName))
	skillsObj, ok := got["skills"].(map[string]any)
	require.True(t, ok, "skills object survives (foreign path remains)")
	assert.NotContains(t, skillsObj["paths"], opencodeSkillDir, "ctxloom's path entry is removed")
	assert.Contains(t, skillsObj["paths"], "/foreign/skills", "foreign path entry survives")
}

// TestSurfaces_SkillsSurface proves the materialize path's SurfaceSet carries
// a skills surface that writes the skill package tree AND registers
// skills.paths, then reverts both on cleanup.
func TestSurfaces_SkillsSurface(t *testing.T) {
	fs := afero.NewMemMapFs()
	set := NewSurfaces(agent.SurfaceInputs{
		Skills: []agent.SkillExport{
			{Name: "humanize", Enabled: true, Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("body"), Mode: 0644}}},
			{Name: "disabled-skill", Enabled: false, Files: []agent.PackageFile{{RelPath: "SKILL.md", Content: []byte("nope")}}},
		},
	}, fs)

	s, err := set.SurfaceFor(agent.SurfaceSkills, agent.ApproachUnsafeFile)
	require.NoError(t, err, "opencode declares a skills surface")

	delivered, err := s.Deliver("/proj")
	require.NoError(t, err)
	skillMD := filepath.Join("/proj", ".opencode", "skill", "humanize", "SKILL.md")
	exists, _ := afero.Exists(fs, skillMD)
	assert.True(t, exists, "skills surface materializes the enabled skill")
	exists, _ = afero.Exists(fs, filepath.Join("/proj", ".opencode", "skill", "disabled-skill", "SKILL.md"))
	assert.False(t, exists, "a disabled skill must not be written")

	got := readJSON(t, fs, filepath.Join("/proj", ConfigFileName))
	skillsObj, ok := got["skills"].(map[string]any)
	require.True(t, ok, "skills.paths registered by the surface")
	assert.Contains(t, skillsObj["paths"], opencodeSkillDir)

	require.NoError(t, delivered.Cleanup())
	exists, _ = afero.Exists(fs, skillMD)
	assert.False(t, exists, "skills surface cleanup reverts the skill")
	cfgExists, _ := afero.Exists(fs, filepath.Join("/proj", ConfigFileName))
	if cfgExists {
		got = readJSON(t, fs, filepath.Join("/proj", ConfigFileName))
		if skillsObj, ok = got["skills"].(map[string]any); ok {
			assert.NotContains(t, skillsObj["paths"], opencodeSkillDir, "cleanup unregisters skills.paths")
		}
	}
}

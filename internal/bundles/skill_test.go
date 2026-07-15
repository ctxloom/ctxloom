package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// SkillPackage parsing tests (Part B, slice B1 — data model, source-tree form)
// =============================================================================
//
// A skill is a PACKAGE (a directory), not a single text blob like a fragment
// or command: SKILL.md (required, YAML frontmatter name+description) plus
// arbitrary sibling files (scripts/, assets, references). ParseSkillPackage is
// the one function that reads that tree off an afero.Fs and turns it into a
// validated SkillPackage with a deterministic per-file manifest. Tests assert
// actual parsed payload (frontmatter fields, hashes, modes) — never just
// "no error" — per the silent-no-op discipline this codebase treats bugs as.

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeSkillFixture builds a valid skill package tree under dir on fsys:
// SKILL.md (frontmatter name/description), an executable scripts/run.sh, and
// a plain asset file. Returns the fixture's exact file bytes so tests can
// assert exact hashes rather than merely "some hash".
func writeSkillFixture(t *testing.T, fsys afero.Fs, dir, name string) map[string][]byte {
	t.Helper()
	require.NoError(t, fsys.MkdirAll(dir+"/scripts", 0755))
	require.NoError(t, fsys.MkdirAll(dir+"/assets", 0755))

	files := map[string][]byte{
		"SKILL.md":        []byte("---\nname: " + name + "\ndescription: Does a thing well.\n---\n\n# " + name + "\n\nInstructions body.\n"),
		"scripts/run.sh":  []byte("#!/bin/sh\necho hi\n"),
		"assets/logo.png": []byte("\x89PNGnotreallyapngbutbytesarebytes"),
	}
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md", files["SKILL.md"], 0644))
	require.NoError(t, afero.WriteFile(fsys, dir+"/scripts/run.sh", files["scripts/run.sh"], 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/assets/logo.png", files["assets/logo.png"], 0644))
	return files
}

func TestParseSkillPackage_ValidPackage(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	files := writeSkillFixture(t, fsys, dir, "humanize")

	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)

	assert.Equal(t, "humanize", pkg.Name)
	assert.Equal(t, "humanize", pkg.Frontmatter.Name)
	assert.Equal(t, "Does a thing well.", pkg.Frontmatter.Description)
	assert.Contains(t, pkg.Body, "Instructions body.")

	require.Len(t, pkg.Manifest, 3)
	byPath := map[string]SkillManifestEntry{}
	for _, e := range pkg.Manifest {
		byPath[e.Path] = e
	}
	require.Contains(t, byPath, "SKILL.md")
	require.Contains(t, byPath, "scripts/run.sh")
	require.Contains(t, byPath, "assets/logo.png")

	assert.Equal(t, sha256Hex(files["SKILL.md"]), byPath["SKILL.md"].SHA256)
	assert.Equal(t, "0644", byPath["SKILL.md"].Mode)

	assert.Equal(t, sha256Hex(files["scripts/run.sh"]), byPath["scripts/run.sh"].SHA256)
	assert.Equal(t, "0755", byPath["scripts/run.sh"].Mode, "the exec bit on scripts/ is load-bearing and must be captured")

	assert.Equal(t, sha256Hex(files["assets/logo.png"]), byPath["assets/logo.png"].SHA256)
	assert.Equal(t, "0644", byPath["assets/logo.png"].Mode)

	// Manifest entries come back sorted by path.
	var paths []string
	for _, e := range pkg.Manifest {
		paths = append(paths, e.Path)
	}
	assert.True(t, sort.StringsAreSorted(paths), "manifest must be sorted by path, got %v", paths)
}

func TestParseSkillPackage_MissingSkillMDErrsLoud(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/empty"
	require.NoError(t, fsys.MkdirAll(dir, 0755))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SKILL.md")
}

func TestParseSkillPackage_NameTooLong(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/toolongname"
	long := strings.Repeat("a", 65)
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: "+long+"\ndescription: d\n---\nbody\n"), 0644))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "64")
}

func TestParseSkillPackage_InvalidNameChars(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/Bad_Name"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: Bad_Name\ndescription: d\n---\nbody\n"), 0644))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lowercase")
}

func TestParseSkillPackage_ReservedWord(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/claude-helper"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: claude-helper\ndescription: d\n---\nbody\n"), 0644))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestParseSkillPackage_NameDirMismatch(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/myskill"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: other-name\ndescription: d\n---\nbody\n"), 0644))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directory")
}

// TestParseSkillPackage_NameDirMatchIsCaseUnderscoreInsensitive pins D5: a
// directory "Financial_Skill" matches frontmatter name "financial-skill".
func TestParseSkillPackage_NameDirMatchIsCaseUnderscoreInsensitive(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/Financial_Skill"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: financial-skill\ndescription: d\n---\nbody\n"), 0644))

	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)
	assert.Equal(t, "financial-skill", pkg.Frontmatter.Name)
}

func TestParseSkillPackage_DescriptionTooLong(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/longdesc"
	long := strings.Repeat("d", 1025)
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: longdesc\ndescription: "+long+"\n---\nbody\n"), 0644))

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1024")
}

// TestParseSkillPackage_PackageTooLarge simulates the <30MB constraint with an
// injected, much smaller limit rather than a real 30MB fixture.
func TestParseSkillPackage_PackageTooLarge(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/big"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: big\ndescription: d\n---\nbody\n"), 0644))
	require.NoError(t, afero.WriteFile(fsys, dir+"/assets/payload.bin",
		[]byte(strings.Repeat("x", 4096)), 0644))

	_, err := ParseSkillPackage(fsys, dir, 1024) // 1KB cap, package is >4KB
	require.Error(t, err)
	assert.Contains(t, err.Error(), "size")
}

func TestParseSkillPackage_DefaultMaxSizeIsThirtyMB(t *testing.T) {
	assert.EqualValues(t, 30*1024*1024, DefaultMaxSkillPackageBytes)
}

// =============================================================================
// SkillManifest determinism tests
// =============================================================================

func TestSkillManifest_SerializeIsDeterministicRegardlessOfInputOrder(t *testing.T) {
	a := SkillManifest{
		{Path: "scripts/run.sh", SHA256: "sha256:aaa", Mode: "0755"},
		{Path: "SKILL.md", SHA256: "sha256:bbb", Mode: "0644"},
		{Path: "assets/logo.png", SHA256: "sha256:ccc", Mode: "0644"},
	}
	b := SkillManifest{
		{Path: "SKILL.md", SHA256: "sha256:bbb", Mode: "0644"},
		{Path: "assets/logo.png", SHA256: "sha256:ccc", Mode: "0644"},
		{Path: "scripts/run.sh", SHA256: "sha256:aaa", Mode: "0755"},
	}

	assert.Equal(t, a.Serialize(), b.Serialize(), "same tree, different insertion order, must serialize identically")
	assert.Equal(t, a.Hash(), b.Hash())
}

func TestSkillManifest_HashChangesWithContent(t *testing.T) {
	a := SkillManifest{{Path: "SKILL.md", SHA256: "sha256:aaa", Mode: "0644"}}
	c := SkillManifest{{Path: "SKILL.md", SHA256: "sha256:different", Mode: "0644"}}
	assert.NotEqual(t, a.Hash(), c.Hash())
}

func TestSkillManifest_SameTreeSameHash(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir1 := "/bundle1/skills/humanize"
	dir2 := "/bundle2/skills/humanize"
	writeSkillFixture(t, fsys, dir1, "humanize")
	writeSkillFixture(t, fsys, dir2, "humanize")

	pkg1, err := ParseSkillPackage(fsys, dir1, 0)
	require.NoError(t, err)
	pkg2, err := ParseSkillPackage(fsys, dir2, 0)
	require.NoError(t, err)

	assert.Equal(t, pkg1.Manifest.Hash(), pkg2.Manifest.Hash(), "identical trees must hash identically")
}

// =============================================================================
// ResolveSkillDir confinement tests
// =============================================================================

func TestResolveSkillDir_DefaultsToSkillsSlashName(t *testing.T) {
	dir, err := ResolveSkillDir("/bundle", "humanize", BundleSkill{})
	require.NoError(t, err)
	assert.Equal(t, "/bundle/skills/humanize", filepath.ToSlash(dir))
}

func TestResolveSkillDir_HonorsExplicitPath(t *testing.T) {
	dir, err := ResolveSkillDir("/bundle", "humanize", BundleSkill{Path: "skills/humanize-v2"})
	require.NoError(t, err)
	assert.Equal(t, "/bundle/skills/humanize-v2", filepath.ToSlash(dir))
}

func TestResolveSkillDir_RejectsPathEscape(t *testing.T) {
	_, err := ResolveSkillDir("/bundle", "evil", BundleSkill{Path: "../../etc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "escapes")
}

func TestResolveSkillDir_RejectsAbsolutePath(t *testing.T) {
	_, err := ResolveSkillDir("/bundle", "evil", BundleSkill{Path: "/etc/passwd"})
	require.Error(t, err)
}

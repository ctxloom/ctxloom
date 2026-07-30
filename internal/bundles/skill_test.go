package bundles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

// TestParseSkillPackage_DefaultMaxSizeGatesRealParsing exercises
// DefaultMaxSkillPackageBytes OPERATIONALLY (not just as a compared literal):
// a package just under the default cap parses; the same default constant used
// as maxBytes<=0's fallback is what buildSkillManifest actually enforces. This
// pins the constant's arithmetic (30 * 1024 * 1024, not e.g. 30 + 1024 + 1024
// or 30 * 1024 + 1024) against real parsing behavior, not just a compile-time
// literal comparison.
func TestParseSkillPackage_DefaultMaxSizeGatesRealParsing(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/nearcap"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md",
		[]byte("---\nname: nearcap\ndescription: d\n---\nbody\n"), 0644))
	// 20MB payload: well under 30*1024*1024 but far over any of the smaller
	// wrong-arithmetic candidates (30*1024=30720, 30*1024+1024=31744, etc).
	big := make([]byte, 20*1024*1024)
	require.NoError(t, afero.WriteFile(fsys, dir+"/assets/payload.bin", big, 0644))

	_, err := ParseSkillPackage(fsys, dir, 0) // maxBytes<=0 -> DefaultMaxSkillPackageBytes
	require.NoError(t, err, "a 20MB package must parse under the real 30MB default cap")
}

// =============================================================================
// validateSkillFrontmatter boundary tests — pin the EXACT `>` limits so a
// CONDITIONALS_BOUNDARY mutant (`>` becoming `>=`) is caught: a name/description
// exactly AT the limit must be accepted, one character over must be rejected.
// =============================================================================

func TestValidateSkillFrontmatter_NameLengthBoundary(t *testing.T) {
	exact := strings.Repeat("a", SkillNameMaxLen)
	err := validateSkillFrontmatter(SkillFrontmatter{Name: exact, Description: "d"}, exact)
	assert.NoError(t, err, "a name exactly at the %d char limit must be accepted", SkillNameMaxLen)

	over := strings.Repeat("a", SkillNameMaxLen+1)
	err = validateSkillFrontmatter(SkillFrontmatter{Name: over, Description: "d"}, over)
	require.Error(t, err, "a name one char OVER the limit must be rejected")
	assert.Contains(t, err.Error(), strconv.Itoa(SkillNameMaxLen))
}

func TestValidateSkillFrontmatter_DescriptionLengthBoundary(t *testing.T) {
	const name = "validname"
	exact := strings.Repeat("d", SkillDescriptionMaxLen)
	err := validateSkillFrontmatter(SkillFrontmatter{Name: name, Description: exact}, name)
	assert.NoError(t, err, "a description exactly at the %d char limit must be accepted", SkillDescriptionMaxLen)

	over := strings.Repeat("d", SkillDescriptionMaxLen+1)
	err = validateSkillFrontmatter(SkillFrontmatter{Name: name, Description: over}, name)
	require.Error(t, err, "a description one char OVER the limit must be rejected")
	assert.Contains(t, err.Error(), strconv.Itoa(SkillDescriptionMaxLen))
}

// TestBuildSkillManifest_PackageSizeBoundary pins buildSkillManifest's running
// total-size accounting AND its `> maxBytes` boundary check together: a
// package whose total bytes are exactly at the cap must parse; one byte over
// must fail loud. A single fixed-size file (no siblings) makes the total
// exact and unambiguous, and a REMOVE_SELF_ASSIGNMENTS mutant on `total +=
// info.Size()` (which would leave total permanently 0) is caught by the
// over-cap case: with no accumulation, the size check could never fire.
func TestBuildSkillManifest_PackageSizeBoundary(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/sizecap"
	require.NoError(t, fsys.MkdirAll(dir, 0755))
	skillMD := []byte("---\nname: sizecap\ndescription: d\n---\nbody\n")
	require.NoError(t, afero.WriteFile(fsys, dir+"/SKILL.md", skillMD, 0644))

	exactCap := int64(len(skillMD))
	_, err := ParseSkillPackage(fsys, dir, exactCap)
	require.NoError(t, err, "total size exactly at the cap must be accepted")

	_, err = ParseSkillPackage(fsys, dir, exactCap-1)
	require.Error(t, err, "total size one byte OVER the cap must be rejected")
	assert.Contains(t, err.Error(), "size")
}

// =============================================================================
// SkillManifest.sorted() ordering test
// =============================================================================

// TestSkillManifest_SortedOrdersByPathAscending exercises sorted()'s
// comparator with several distinct, interleaved paths (not merely two
// entries), pinning that the result is strictly ascending by Path.
func TestSkillManifest_SortedOrdersByPathAscending(t *testing.T) {
	m := SkillManifest{
		{Path: "zzz/last.txt", SHA256: "sha256:z"},
		{Path: "SKILL.md", SHA256: "sha256:s"},
		{Path: "assets/logo.png", SHA256: "sha256:a"},
		{Path: "middle/file.txt", SHA256: "sha256:m"},
		{Path: "assets/aaa.txt", SHA256: "sha256:aa"},
	}
	out := m.sorted()
	var paths []string
	for _, e := range out {
		paths = append(paths, e.Path)
	}
	assert.Equal(t, []string{
		"SKILL.md",
		"assets/aaa.txt",
		"assets/logo.png",
		"middle/file.txt",
		"zzz/last.txt",
	}, paths)
}

// =============================================================================
// safeSkillRelJoin direct unit tests — every rejection branch pinned
// individually so an INVERT_LOGICAL mutant on any || in the confinement check
// must flip at least one case.
// =============================================================================

func TestSafeSkillRelJoin(t *testing.T) {
	tests := []struct {
		name       string
		rel        string
		wantOK     bool
		wantSuffix string // when wantOK, the expected path suffix (forward-slash form)
	}{
		{"empty rel is rejected", "", false, ""},
		{"absolute unix path is rejected", "/etc/passwd", false, ""},
		{"leading dotdot is rejected", "../escape", false, ""},
		{"dotdot in the middle is rejected", "sub/../../escape", false, ""},
		{"bare dotdot segment is rejected", "a/../b", false, ""},
		{"trailing dotdot segment is rejected", "a/b/..", false, ""},
		{"simple multi-segment relative path is accepted", "skills/humanize", true, "bundle/skills/humanize"},
		{"single segment is accepted", "humanize", true, "bundle/humanize"},
		{"leading dot segment is accepted (not an escape)", "./humanize", true, "bundle/humanize"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := safeSkillRelJoin("/bundle", tt.rel)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.True(t, strings.HasSuffix(filepath.ToSlash(got), tt.wantSuffix), "got %q, want suffix %q", got, tt.wantSuffix)
			} else {
				assert.Empty(t, got)
			}
		})
	}
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

// =============================================================================
// U031 findings-sweep additions
// =============================================================================

// openFailFs fails Open for one exact path, so a test can make a single file in
// an otherwise-valid skill tree unreadable without depending on real filesystem
// permissions (afero.ReadFile goes through Open).
type openFailFs struct {
	afero.Fs
	failPath string
}

func (f *openFailFs) Open(name string) (afero.File, error) {
	if filepath.ToSlash(name) == f.failPath {
		return nil, fmt.Errorf("simulated permission denied")
	}
	return f.Fs.Open(name)
}

// TestBuildSkillManifest_MidWalkFailureNamesTheSkillAndFile is U031-F18.
//
// buildSkillManifest returned walkErr, relErr and readErr completely
// unwrapped, so a mid-walk failure surfaced as a bare "simulated permission
// denied" with no indication of which skill was being parsed or which file
// failed — while the size-cap check two lines away carries `skill directory
// %q`. ParseSkillPackage is reached from bundle loading, skill sync, export and
// install, so the caller has no other way to know which of a bundle's skills
// broke.
func TestBuildSkillManifest_MidWalkFailureNamesTheSkillAndFile(t *testing.T) {
	mem := afero.NewMemMapFs()
	dir := "/src/skills/humanize"
	writeSkillFixture(t, mem, dir, "humanize")

	fsys := &openFailFs{Fs: mem, failPath: dir + "/scripts/run.sh"}

	_, err := ParseSkillPackage(fsys, dir, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "humanize",
		"the failing skill must be named: a bundle can ship many skills and only one of them broke")
	assert.Contains(t, err.Error(), "scripts/run.sh",
		"the failing file must be named, relative to the package root")
	assert.Contains(t, err.Error(), "simulated permission denied",
		"the underlying cause must still be wrapped, not replaced")
}

// TestSkillManifestSerializeFallback_IsNotSharedAcrossManifests is U031-F06.
//
// Serialize's error fallback is a SIGNATURE PREIMAGE: PublisherSkillSignature
// Verifier.VerifyManifestSignature verifies a detached signature over exactly
// these bytes, and operations.ExportSkill signs exactly these bytes. One
// constant standing in for every manifest therefore means one signature would
// verify against ANY manifest that hit the fallback — which is precisely the
// defect the sibling BundleMCP/BundleHook fallbacks call out in their own
// comments ("to a digest DISTINCT per server/failure, not a shared constant:
// one constant standing in for many different items is the T4 defect"), even
// though this comment claimed to be following that precedent.
//
// Different manifests must produce different fallback bytes.
func TestSkillManifestSerializeFallback_IsNotSharedAcrossManifests(t *testing.T) {
	a := SkillManifest{{Path: "SKILL.md", SHA256: "sha256:aaa", Mode: "0644"}}
	b := SkillManifest{{Path: "scripts/run.sh", SHA256: "sha256:bbb", Mode: "0755"}}

	boom := errors.New("simulated marshal failure")
	assert.NotEqual(t, string(skillManifestSerializeFallback(a, boom)), string(skillManifestSerializeFallback(b, boom)),
		"two different manifests must not share one preimage — a signature over that preimage would cover both")
	assert.Equal(t, string(skillManifestSerializeFallback(a, boom)), string(skillManifestSerializeFallback(a, boom)),
		"the fallback must still be deterministic for a given manifest")
}

// TestSkillManifestEntry_HoldsOnlyStringsSoMarshalCannotFail is the MEASURED
// reason Serialize's error branch is unreachable today, turned into a gate:
// encoding/json cannot fail on a slice of structs whose every field is a
// string, so no live signature can carry the fallback preimage. This test goes
// red the moment a field of some other kind (a channel, a func, a
// map[interface{}]…) is added — i.e. the moment the branch becomes reachable
// and its bytes start to matter.
func TestSkillManifestEntry_HoldsOnlyStringsSoMarshalCannotFail(t *testing.T) {
	typ := reflect.TypeOf(SkillManifestEntry{})
	require.Positive(t, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		assert.Equal(t, reflect.String, f.Type.Kind(),
			"field %s is not a string: json.Marshal can now fail, so Serialize's fallback preimage is reachable and must be reviewed", f.Name)
	}
}

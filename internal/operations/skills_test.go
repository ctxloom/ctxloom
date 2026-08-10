package operations

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// This file is the TDD suite for `ctxloom skill` core operations:
// create scaffolds a package ParseSkillPackage/the loader accepts; sync
// writes a manifest whose hashes match the tree and updates on edit; export
// then import round-trips byte-identically with the exec bit intact; import
// rejects a path-traversal archive; and (skill curation itself is covered in
// internal/lm/backends/skill_curation_test.go, which needs the profile
// resolver this package's cfg does not wire standalone).

// writeDirFormBundle creates an empty directory-form bundle (bundle.yaml with
// no items yet) at .ctxloom/content/bundles/<name>/bundle.yaml — the
// prerequisite CreateSkill/SyncSkill/ExportSkill/ImportSkill all require
// (skills are unsupported in a single-file bundle).
func writeDirFormBundle(t *testing.T, appDir, name string) {
	t.Helper()
	dir := filepath.Join(paths.LocalBundlesPath(appDir), name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bundle.yaml"),
		[]byte("name: "+name+"\nversion: \"1.0\"\n"), 0o644))
}

func TestCreateSkill_ScaffoldsValidPackage(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")

	res, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{
		Bundle: "b", Name: "reviewer", Description: "Reviews things.",
	})
	require.NoError(t, err)
	assert.Equal(t, "created", res.Status)
	assert.Equal(t, "reviewer", res.Name)

	// The scaffold must independently parse via a fresh ParseSkillPackage
	// call over the real OS filesystem — the create-time acceptance gate
	// CreateSkill itself already ran, re-proven here as the test's own
	// assertion rather than trusting CreateSkill's internal check.
	realPkg, err := bundles.ParseSkillPackage(afero.NewOsFs(), res.Dir, 0)
	require.NoError(t, err)
	assert.Equal(t, "reviewer", realPkg.Frontmatter.Name)
	assert.Equal(t, "Reviews things.", realPkg.Frontmatter.Description)

	// bundle.yaml must register the new skill so the loader/list surfaces it.
	loaded, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	_, ok := loaded.Skills["reviewer"]
	assert.True(t, ok, "CreateSkill must register the skill in bundle.yaml's skills: map")

	// Re-creating the same name is refused, never silently overwritten.
	_, err = CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "b", Name: "reviewer"})
	require.ErrorIs(t, err, ErrItemExists)
}

// TestRemoveSkill_DeletesDirectoryAndBundleEntry proves the full effect: the
// bundle.yaml `skills:` registration is gone AND the on-disk skill directory
// tree is gone, not just one half — a skill package can be created and
// (until this) never removed.
func TestRemoveSkill_DeletesDirectoryAndBundleEntry(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")

	created, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{
		Bundle: "b", Name: "reviewer", Description: "Reviews things.",
	})
	require.NoError(t, err)
	require.DirExists(t, created.Dir)

	res, err := RemoveSkill(context.Background(), cfg, RemoveSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)
	assert.Equal(t, "removed", res.Status)
	assert.Equal(t, created.Dir, res.Dir)

	loaded, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	_, ok := loaded.Skills["reviewer"]
	assert.False(t, ok, "the skills: registration must be gone")

	_, statErr := os.Stat(created.Dir)
	assert.True(t, os.IsNotExist(statErr), "the skill's directory tree must be gone from disk")
}

// TestRemoveSkill_UnknownNameIsNotFound: removing a skill the bundle never
// declared is an error, never a silent zero-effect success.
func TestRemoveSkill_UnknownNameIsNotFound(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")

	_, err := RemoveSkill(context.Background(), cfg, RemoveSkillRequest{Bundle: "b", Name: "nope"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

// TestRemoveSkill_BundleYAMLUpdatedEvenIfDirRemovalFails proves the write
// ORDER: the registration is dropped from bundle.yaml FIRST, so a failure
// removing the directory afterward still leaves `skill list` honest (never
// pointing at a directory that is about to vanish) rather than a
// half-applied state where the registration survives pointing at nothing.
func TestRemoveSkill_BundleYAMLUpdatedEvenIfDirRemovalFails(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")

	created, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)

	// A read-only PARENT directory makes the child directory's removal fail
	// (permission denied) without needing a fake filesystem for the whole
	// call — RemoveSkill's fs write to bundle.yaml happens first and must
	// still land.
	parent := filepath.Dir(created.Dir)
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	_, err = RemoveSkill(context.Background(), cfg, RemoveSkillRequest{Bundle: "b", Name: "reviewer"})
	require.Error(t, err, "a directory-removal failure must be reported, not swallowed")

	require.NoError(t, os.Chmod(parent, 0o755))
	loaded, lerr := bundleLoader(cfg).Load("b")
	require.NoError(t, lerr)
	_, ok := loaded.Skills["reviewer"]
	assert.False(t, ok, "bundle.yaml's registration must already be gone even though the directory removal failed")
}

func TestCreateSkill_RefusesSingleFileBundle(t *testing.T) {
	cfg := newItemTestBundle(t) // "b" is single-file (name.yaml), per CreateBundle

	_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "b", Name: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "directory-form bundle")
}

func TestSyncSkill_WritesManifestMatchingTree_AndUpdatesHashOnEdit(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")
	createRes, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)

	// A freshly-created skill has no `files:` manifest yet (create doesn't
	// sync); the first sync populates it and MUST report Changed (empty ->
	// populated).
	syncRes, err := SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)
	require.Len(t, syncRes.Synced, 1)
	assert.True(t, syncRes.Synced[0].Changed, "first sync populates an empty manifest, which counts as changed")
	assert.Equal(t, 1, syncRes.Synced[0].FileCount, "only SKILL.md exists so far")

	skillMDPath := filepath.Join(createRes.Dir, "SKILL.md")
	data, err := os.ReadFile(skillMDPath)
	require.NoError(t, err)
	wantHash := bundles.HashPayload(data)

	loaded, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	entry := loaded.Skills["reviewer"]
	require.Contains(t, entry.Files, "SKILL.md")
	assert.Equal(t, wantHash, entry.Files["SKILL.md"].SHA256, "the recorded manifest hash must match the actual on-disk SKILL.md bytes")
	assert.Equal(t, "0644", entry.Files["SKILL.md"].Mode)

	// Re-syncing an UNCHANGED tree must report Changed == false (idempotent).
	syncRes2, err := SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)
	require.Len(t, syncRes2.Synced, 1)
	assert.False(t, syncRes2.Synced[0].Changed, "syncing an unchanged tree must not report a change")

	// Editing SKILL.md and re-syncing MUST update the recorded hash.
	edited := append(data, []byte("\nMore instructions.\n")...)
	require.NoError(t, os.WriteFile(skillMDPath, edited, 0o644))
	syncRes3, err := SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "b", Name: "reviewer"})
	require.NoError(t, err)
	require.Len(t, syncRes3.Synced, 1)
	assert.True(t, syncRes3.Synced[0].Changed, "editing a file must be detected as a manifest change")

	loaded2, err := bundleLoader(cfg).Load("b")
	require.NoError(t, err)
	entry2 := loaded2.Skills["reviewer"]
	assert.Equal(t, bundles.HashPayload(edited), entry2.Files["SKILL.md"].SHA256, "the manifest must be updated to the edited content's hash")
	assert.NotEqual(t, wantHash, entry2.Files["SKILL.md"].SHA256)
}

func TestSyncSkill_UnknownNameIsNotFound(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "b")
	_, err := SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "b", Name: "nope"})
	require.ErrorIs(t, err, ErrItemNotFound)
}

// TestExportImportSkill_RoundTrip_ByteIdenticalTreeAndExecBit proves export
// (tree -> Anthropic-shaped zip) then import (zip -> tree) round-trips every
// file's exact bytes and the scripts/ exec bit, into a DIFFERENT bundle —
// the archive interchange guarantee, now reachable end to end through the
// live CLI-facing operations.
func TestExportImportSkill_RoundTrip_ByteIdenticalTreeAndExecBit(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "src")
	writeDirFormBundle(t, appDir, "dst")

	createRes, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "src", Name: "reviewer", Description: "Reviews Go diffs."})
	require.NoError(t, err)
	scriptPath := filepath.Join(createRes.Dir, "scripts", "run.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(scriptPath), 0o755))
	require.NoError(t, os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi\n"), 0o755))
	_, err = SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)

	origSkillMD, err := os.ReadFile(filepath.Join(createRes.Dir, "SKILL.md"))
	require.NoError(t, err)
	origScript, err := os.ReadFile(scriptPath)
	require.NoError(t, err)

	zipPath := filepath.Join(t.TempDir(), "reviewer.zip")
	expRes, err := ExportSkill(context.Background(), cfg, ExportSkillRequest{Bundle: "src", Name: "reviewer", OutPath: zipPath})
	require.NoError(t, err)
	assert.Equal(t, zipPath, expRes.ZipPath)
	assert.Empty(t, expRes.SigPath, "no --sign requested, so no .sig sibling")

	impRes, err := ImportSkill(context.Background(), cfg, ImportSkillRequest{Bundle: "dst", ArchivePath: zipPath})
	require.NoError(t, err)
	assert.Equal(t, "imported", impRes.Status)
	assert.Equal(t, "reviewer", impRes.Name)
	assert.Equal(t, "unsigned", impRes.SignatureState, "no signature was supplied")
	assert.Equal(t, 2, impRes.FileCount, "SKILL.md + scripts/run.sh")

	gotSkillMD, err := os.ReadFile(filepath.Join(impRes.Dir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, origSkillMD, gotSkillMD, "SKILL.md bytes must round-trip byte-identical")

	gotScriptPath := filepath.Join(impRes.Dir, "scripts", "run.sh")
	gotScript, err := os.ReadFile(gotScriptPath)
	require.NoError(t, err)
	assert.Equal(t, origScript, gotScript, "scripts/run.sh bytes must round-trip byte-identical")

	info, err := os.Stat(gotScriptPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "the exec bit must survive export -> import")

	// The destination bundle must have a fresh, matching manifest recorded.
	loaded, err := bundleLoader(cfg).Load("dst")
	require.NoError(t, err)
	entry, ok := loaded.Skills["reviewer"]
	require.True(t, ok)
	assert.Equal(t, bundles.HashPayload(origScript), entry.Files["scripts/run.sh"].SHA256)
	assert.Equal(t, "0755", entry.Files["scripts/run.sh"].Mode)
}

// TestExportImportSkill_SignedRoundTrip_VerifiesAgainstTrustedPublisher wires
// PublisherSkillSignatureVerifier end to end: export --sign writes a detached
// signature over the manifest; import, given that .sig and a trust root that
// trusts the signing key, reports SignatureState "verified".
func TestExportImportSkill_SignedRoundTrip_VerifiesAgainstTrustedPublisher(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "src")
	writeDirFormBundle(t, appDir, "dst")

	_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)
	_, err = SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	zipPath := filepath.Join(t.TempDir(), "reviewer.zip")
	expRes, err := ExportSkill(context.Background(), cfg, ExportSkillRequest{
		Bundle: "src", Name: "reviewer", OutPath: zipPath, Sign: true, Signer: signer,
	})
	require.NoError(t, err)
	require.NotEmpty(t, expRes.SigPath)

	trusted := allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{"reviewer@example.com"},
		KeyType:    signer.PublicKey().Type(),
		PublicKey:  signer.PublicKey(),
	})

	impRes, err := ImportSkill(context.Background(), cfg, ImportSkillRequest{
		Bundle: "dst", ArchivePath: zipPath, SigPath: expRes.SigPath, Root: trusted,
	})
	require.NoError(t, err)
	assert.Equal(t, "verified", impRes.SignatureState)

	untrusted := allowedsigners.NewStore() // no keys at all
	// Re-import (over the same name) with a trust root that does NOT trust
	// the signing key: the import still LANDS (never auto-rejected for lack
	// of trust) but is reported unverified, never silently "verified".
	impRes2, err := ImportSkill(context.Background(), cfg, ImportSkillRequest{
		Bundle: "dst", ArchivePath: zipPath, SigPath: expRes.SigPath, Root: untrusted,
	})
	require.NoError(t, err, "an untrusted-publisher signature must not block the import")
	assert.Contains(t, impRes2.SignatureState, "unverified", "a signature by an untrusted key must not be reported as verified")
}

// failingSigner is an ssh.Signer whose Sign always errors — a hermetic double
// for a signing backend that rejects the request (revoked key, hardware token
// unplugged, agent forwarding down), letting ExportSkill's --sign failure path
// be exercised without a real cryptographic failure mode.
type failingSigner struct{ pub ssh.PublicKey }

func (f failingSigner) PublicKey() ssh.PublicKey { return f.pub }
func (f failingSigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return nil, fmt.Errorf("signing backend unavailable")
}

// TestExportSkill_RefusesToOverwriteWithoutForce: ExportSkill used to
// silently clobber any existing file at the output path — the default
// "<name>.zip" lands in the process cwd, so a second export (or any
// unrelated file already using that name) was destroyed with no warning.
func TestExportSkill_RefusesToOverwriteWithoutForce(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "src")
	_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)
	_, err = SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)

	zipPath := filepath.Join(t.TempDir(), "reviewer.zip")
	require.NoError(t, os.WriteFile(zipPath, []byte("PRE-EXISTING, NOT A REAL ZIP"), 0o644))

	_, err = ExportSkill(context.Background(), cfg, ExportSkillRequest{Bundle: "src", Name: "reviewer", OutPath: zipPath})
	require.Error(t, err, "must refuse to overwrite an existing file without Force")
	assert.Contains(t, err.Error(), "already exists")

	still, rerr := os.ReadFile(zipPath)
	require.NoError(t, rerr)
	assert.Equal(t, "PRE-EXISTING, NOT A REAL ZIP", string(still), "the existing file must survive a refused export")

	// Force explicitly opts into the overwrite.
	res, err := ExportSkill(context.Background(), cfg, ExportSkillRequest{Bundle: "src", Name: "reviewer", OutPath: zipPath, Force: true})
	require.NoError(t, err, "Force must allow the overwrite")
	assert.Equal(t, zipPath, res.ZipPath)
}

// TestExportSkill_SignFailureLeavesNoPartialZip: a
// --sign failure used to leave the just-written zip on disk, unsigned, with no
// indication anything had gone wrong — a caller retrying (or just listing the
// directory) would find a zip indistinguishable from a successful unsigned
// export.
func TestExportSkill_SignFailureLeavesNoPartialZip(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "src")
	_, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)
	_, err = SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "src", Name: "reviewer"})
	require.NoError(t, err)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	realSigner, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	zipPath := filepath.Join(t.TempDir(), "reviewer.zip")
	_, err = ExportSkill(context.Background(), cfg, ExportSkillRequest{
		Bundle: "src", Name: "reviewer", OutPath: zipPath, Sign: true,
		Signer: failingSigner{pub: realSigner.PublicKey()},
	})
	require.Error(t, err)

	_, statErr := os.Stat(zipPath)
	assert.True(t, os.IsNotExist(statErr), "a failed --sign export must leave no zip behind, not an unsigned one")
}

// maliciousZipBytes builds a zip whose single entry escapes its own
// directory via a path-traversal segment — the same zip-slip shape
// HardenedExtract rejects (bundles/skill_archive_test.go covers the
// extractor exhaustively; this is the smoke-level proof that ImportSkill
// actually routes through it and refuses acceptance).
func maliciousZipBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("evil/../../../etc/passwd-clobber")
	require.NoError(t, err)
	_, err = w.Write([]byte("pwned"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func TestImportSkill_RejectsPathTraversalArchive(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "dst")

	archivePath := filepath.Join(t.TempDir(), "evil.zip")
	require.NoError(t, os.WriteFile(archivePath, maliciousZipBytes(t), 0o644))

	_, err := ImportSkill(context.Background(), cfg, ImportSkillRequest{Bundle: "dst", ArchivePath: archivePath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traversal")

	// Nothing must have landed: the bundle gains no skills entry, and the
	// skills/ directory (if created at all) holds nothing from the attack.
	loaded, err := bundleLoader(cfg).Load("dst")
	require.NoError(t, err)
	assert.Empty(t, loaded.Skills, "a rejected archive must leave the bundle's skills: map untouched")
}

// writeMalformedSkillZip writes a zip whose top-level directory is topDir and
// whose SKILL.md has no valid frontmatter, so ParseSkillPackage rejects it
// AFTER extraction. Returns the archive path.
func writeMalformedSkillZip(t *testing.T, topDir string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "malformed.zip")
	f, err := os.Create(path)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w, err := zw.Create(topDir + "/SKILL.md")
	require.NoError(t, err)
	_, err = w.Write([]byte("this is not a skill package\n"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())
	return path
}

// TestImportSkill_MalformedArchiveLeavesTheExistingSkillIntact:
// ImportSkillArchive computed the destination from the ARCHIVE's own top-level
// directory name and RemoveAll'd it before anything validated the replacement,
// so importing a malformed archive over a good skill left the skill gone from
// disk while bundle.yaml still referenced it. The assertion is on the surviving
// BYTES, not on the error — a refusal that has already destroyed the thing it
// refused is the failure mode.
func TestImportSkill_MalformedArchiveLeavesTheExistingSkillIntact(t *testing.T) {
	appDir, cfg := setupBundleTestDir(t)
	writeDirFormBundle(t, appDir, "dst")

	createRes, err := CreateSkill(context.Background(), cfg, CreateSkillRequest{Bundle: "dst", Name: "reviewer", Description: "Reviews Go diffs."})
	require.NoError(t, err)
	_, err = SyncSkill(context.Background(), cfg, SyncSkillRequest{Bundle: "dst", Name: "reviewer"})
	require.NoError(t, err)

	good, err := os.ReadFile(filepath.Join(createRes.Dir, "SKILL.md"))
	require.NoError(t, err)

	_, err = ImportSkill(context.Background(), cfg, ImportSkillRequest{
		Bundle:      "dst",
		ArchivePath: writeMalformedSkillZip(t, "reviewer"),
	})
	require.Error(t, err, "a malformed archive must be refused")

	still, rerr := os.ReadFile(filepath.Join(createRes.Dir, "SKILL.md"))
	require.NoError(t, rerr, "the existing skill tree must survive a refused import")
	assert.Equal(t, good, still, "the existing skill's bytes must be untouched by a refused import")

	// And the bundle's reference must still resolve to a real tree.
	loaded, lerr := bundleLoader(cfg).Load("dst")
	require.NoError(t, lerr)
	entry, ok := loaded.Skills["reviewer"]
	require.True(t, ok, "bundle.yaml must still reference the surviving skill")
	assert.Contains(t, entry.Files, "SKILL.md")
}

// TestSkillReadPaths_NilConfigReturnsErrorNotPanic: ListSkills,
// GetSkill, and ExportSkill used to panic on a nil *config.Config (bundleLoader/
// exposureLoader dereference it immediately), while every mutating sibling in
// this file (CreateSkill, SyncSkill, ImportSkill — via loadBundleForUpdate) and
// ResolveSetupPrompt already reject a nil cfg with a plain error. A read-only
// caller with no project config configured must fail the same way, not crash
// the process.
func TestSkillReadPaths_NilConfigReturnsErrorNotPanic(t *testing.T) {
	_, err := ListSkills(context.Background(), nil, ListSkillsRequest{})
	require.Error(t, err, "ListSkills must reject a nil cfg with an error, not panic")
	assert.Contains(t, err.Error(), "no .ctxloom directory configured")

	_, err = GetSkill(context.Background(), nil, GetSkillRequest{Name: "whatever"})
	require.Error(t, err, "GetSkill must reject a nil cfg with an error, not panic")
	assert.Contains(t, err.Error(), "no .ctxloom directory configured")

	_, err = ExportSkill(context.Background(), nil, ExportSkillRequest{Name: "whatever"})
	require.Error(t, err, "ExportSkill must reject a nil cfg with an error, not panic")
	assert.Contains(t, err.Error(), "no .ctxloom directory configured")
}

// TestSkillTemplate_MarshalFailureFallbackDeleted: the
// "unreachable" yaml.Marshal-failure fallback in skillTemplate built its SKILL.md
// frontmatter via naive fmt.Sprintf string interpolation — exactly the injection
// the function's own doc comment says yaml.Marshal exists to prevent. Since
// SkillFrontmatter holds only strings/slices/maps, yaml.Marshal cannot fail here,
// so the branch bought nothing and stood as a live contradiction of the file's
// own invariant. This test proves the ordinary (marshal-succeeds) path still
// safely handles a description containing the exact character sequence
// (colon-space) that would break naive interpolation.
func TestSkillTemplate_MarshalFailureFallbackDeleted(t *testing.T) {
	out := skillTemplate("my-skill", "TODO: describe this, dangerously")
	assert.Contains(t, out, `description: 'TODO: describe this, dangerously'`,
		"yaml.Marshal must quote a colon-space-bearing description, never hand it to naive fmt interpolation")
	// Round-trip: a real YAML parse of the frontmatter must recover the exact
	// description — the naive fmt.Sprintf fallback this test guards against
	// would have produced a colon that breaks the plain scalar and misparses.
	fm := strings.SplitN(out, "---\n", 3)[1]
	var parsed struct {
		Description string `yaml:"description"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(fm), &parsed))
	assert.Equal(t, "TODO: describe this, dangerously", parsed.Description)
}

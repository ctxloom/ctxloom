package bundles

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
)

// =============================================================================
// Skill archive codec + hardened extractor tests (Part B, slice B1b)
// =============================================================================
//
// The extractor is ADVERSARIAL-INPUT-FACING: an imported skill archive may
// come from an untrusted remote, and a skill's scripts/ files are executable
// once materialized. Every test here asserts PAYLOAD — files present/absent
// on disk, exec bits, byte content — never merely "an error was returned",
// per this codebase's silent-no-op discipline. The malicious-archive fixtures
// below are built by hand with archive/zip and archive/tar directly (bypassing
// this package's own pack path) so they exercise exactly the adversarial
// shapes a hostile archive could take, not just what ExportSkillZip would ever
// produce.

// -----------------------------------------------------------------------------
// Fixture builders
// -----------------------------------------------------------------------------

// buildValidSkillZip builds a valid skill fixture tree, parses it, and packs
// it with ExportSkillZip, returning the zip bytes, the parsed source package
// (whose Manifest is the "signed" reference other tests tamper against), and
// the fixture's exact file bytes.
func buildValidSkillZip(t *testing.T, name string) ([]byte, *SkillPackage, map[string][]byte) {
	t.Helper()
	fsys := afero.NewMemMapFs()
	dir := "/src/skills/" + name
	files := writeSkillFixture(t, fsys, dir, name)

	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)

	zipBytes, err := ExportSkillZip(fsys, dir, pkg)
	require.NoError(t, err)

	return zipBytes, pkg, files
}

type rawZipEntry struct {
	name    string
	content []byte
	mode    os.FileMode
	symlink bool
}

// buildRawZip constructs a zip by hand, independent of ExportSkillZip, so
// tests can build archive shapes ExportSkillZip would never itself produce
// (traversal, absolute paths, symlinks).
func buildRawZip(t *testing.T, entries []rawZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.symlink:
			hdr.SetMode(os.ModeSymlink | 0o777)
		case e.mode != 0:
			hdr.SetMode(e.mode)
		default:
			hdr.SetMode(0o644)
		}
		w, err := zw.CreateHeader(hdr)
		require.NoError(t, err)
		_, err = w.Write(e.content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

type rawTarEntry struct {
	name     string
	content  []byte
	typeflag byte
	linkname string
	mode     int64
}

// buildRawTarGz constructs a tar.gz by hand so tests can carry tar-only
// adversarial shapes (symlinks, hardlinks, device nodes) that zip cannot
// represent.
func buildRawTarGz(t *testing.T, entries []rawTarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typeflag := e.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o644
		}
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: typeflag,
			Linkname: e.linkname,
			Mode:     mode,
			Size:     int64(len(e.content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if len(e.content) > 0 {
			_, err := tw.Write(e.content)
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// -----------------------------------------------------------------------------
// ADVERSARIAL: path traversal / absolute paths (zip-slip)
// -----------------------------------------------------------------------------

func TestHardenedExtract_RejectsZipSlipDotDotTraversal(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "../../etc/evil", content: []byte("pwned")},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traversal")

	exists, _ := afero.Exists(fsys, "/etc/evil")
	assert.False(t, exists, "the escape must not write outside the extraction root")
	entries, _ := afero.ReadDir(fsys, "/out/skill")
	assert.Empty(t, entries, "nothing should land inside the extraction root either")
}

func TestHardenedExtract_RejectsAbsolutePathZip(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "/etc/evil", content: []byte("pwned")},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")

	exists, _ := afero.Exists(fsys, "/etc/evil")
	assert.False(t, exists)
	entries, _ := afero.ReadDir(fsys, "/out/skill")
	assert.Empty(t, entries)
}

func TestHardenedExtract_RejectsSecondTopLevelDirectory(t *testing.T) {
	// A well-formed skill entry followed by an entry rooted at a DIFFERENT
	// top-level directory — not a ".." escape, but still an attempt to smuggle
	// files outside the single skill directory the archive claims to be.
	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		{name: "other/evil.txt", content: []byte("pwned")},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "top-level directory")

	exists, _ := afero.Exists(fsys, "/out/skill/evil.txt")
	assert.False(t, exists)
	otherExists, _ := afero.Exists(fsys, "/out/other")
	assert.False(t, otherExists)
}

// -----------------------------------------------------------------------------
// ADVERSARIAL: symlinks (tar) — default reject, unconditionally
// -----------------------------------------------------------------------------

func TestHardenedExtract_RejectsTarSymlinkEscape(t *testing.T) {
	archive := buildRawTarGz(t, []rawTarEntry{
		{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		{name: "myskill/evil-link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	exists, _ := afero.Exists(fsys, "/out/skill/evil-link")
	assert.False(t, exists, "no symlink, or anything else, should be created for the rejected entry")
	linkTargetExists, _ := afero.Exists(fsys, "/etc/passwd")
	assert.False(t, linkTargetExists)
}

func TestHardenedExtract_RejectsTarSymlinkRelativeEscape(t *testing.T) {
	archive := buildRawTarGz(t, []rawTarEntry{
		{name: "myskill/evil-link", typeflag: tar.TypeSymlink, linkname: "../../escape"},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	exists, _ := afero.Exists(fsys, "/out/skill/evil-link")
	assert.False(t, exists)
}

// TestHardenedExtract_RejectsZipSymlinkEntry is the regression guard the B2
// adversarial review flagged: buildRawZip's `symlink` field (a UNIX-attribute
// symlink mode set directly on a zip.FileHeader, the shape a real symlink-
// carrying zip — e.g. produced by Python's zipfile or `zip -y` — would take)
// was constructed but never actually exercised by any zip test; every
// existing symlink test above uses a TAR entry instead. Zip's symlink
// classification (zipEntryKind, checking os.ModeSymlink in the header's
// external attributes) is a distinct code path from tar's and needs its own
// cheap regression guard for this security invariant.
func TestHardenedExtract_RejectsZipSymlinkEntry(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		{name: "myskill/evil-link", symlink: true},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	exists, _ := afero.Exists(fsys, "/out/skill/evil-link")
	assert.False(t, exists, "no symlink of any kind should ever be created by a zip archive either")
}

// -----------------------------------------------------------------------------
// ADVERSARIAL: hardlinks + device/special files (tar)
// -----------------------------------------------------------------------------

func TestHardenedExtract_RejectsTarHardlinkAndDeviceNodes(t *testing.T) {
	tests := []struct {
		name     string
		typeflag byte
	}{
		{"hardlink", tar.TypeLink},
		{"char-device", tar.TypeChar},
		{"block-device", tar.TypeBlock},
		{"fifo", tar.TypeFifo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := buildRawTarGz(t, []rawTarEntry{
				{name: "myskill/evil", typeflag: tt.typeflag, linkname: "/etc/passwd"},
			})
			fsys := afero.NewMemMapFs()

			_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "special file")

			exists, _ := afero.Exists(fsys, "/out/skill/evil")
			assert.False(t, exists, "no special file of any kind should ever be created")
		})
	}
}

// -----------------------------------------------------------------------------
// ADVERSARIAL: decompression / entry-count bombs
// -----------------------------------------------------------------------------

func TestHardenedExtract_RejectsZipBombOverByteCap(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 5000)
	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		{name: "myskill/assets/big.bin", content: big},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxTotalBytes: 1024})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompression-bomb")

	exists, _ := afero.Exists(fsys, "/out/skill/assets/big.bin")
	assert.False(t, exists, "the oversized entry must never be committed to disk — the cap gates the write, not just the return value")
}

func TestHardenedExtract_RejectsEntryCountBomb(t *testing.T) {
	entries := make([]rawZipEntry, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, rawZipEntry{name: fmt.Sprintf("myskill/file%03d.txt", i), content: []byte("x")})
	}
	archive := buildRawZip(t, entries)
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxEntries: 10})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "entry-count bomb")

	written, _ := afero.ReadDir(fsys, "/out/skill")
	assert.Empty(t, written, "an entry-count bomb must be rejected before a single file is written, not partway through")
}

// -----------------------------------------------------------------------------
// HAPPY PATH: export -> import round trip
// -----------------------------------------------------------------------------

func TestSkillArchive_ExportZipIsAnthropicShapedSingleTopLevelDir(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	require.NotEmpty(t, zr.File)
	for _, f := range zr.File {
		assert.True(t, strings.HasPrefix(f.Name, "humanize/"),
			"every zip entry must live under the single top-level %q dir, got %q", "humanize/", f.Name)
	}
}

func TestSkillArchive_ExportZipIsDeterministic(t *testing.T) {
	zip1, _, _ := buildValidSkillZip(t, "humanize")
	zip2, _, _ := buildValidSkillZip(t, "humanize")
	assert.Equal(t, zip1, zip2, "packing the same tree twice must produce byte-identical zip output")
}

func TestSkillArchive_ExportImportRoundTrip_TreeByteIdenticalAndExecBitPreserved(t *testing.T) {
	zipBytes, srcPkg, files := buildValidSkillZip(t, "humanize")

	destFs := afero.NewMemMapFs()
	finalDir, err := ImportSkillArchive(destFs, zipBytes, "/imported", ExtractOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/imported/humanize", finalDir)

	imported, err := ParseSkillPackage(destFs, finalDir, 0)
	require.NoError(t, err)
	assert.Equal(t, srcPkg.Manifest.Hash(), imported.Manifest.Hash(), "manifest hash must match exactly across export -> import")
	assert.Equal(t, srcPkg.Frontmatter, imported.Frontmatter)
	assert.Equal(t, srcPkg.Body, imported.Body)

	for relPath, want := range files {
		got, err := afero.ReadFile(destFs, filepath.Join(finalDir, relPath))
		require.NoError(t, err, "file %s must exist after import", relPath)
		assert.Equal(t, want, got, "file %s must round-trip byte-identical", relPath)
	}

	scriptInfo, err := destFs.Stat(filepath.Join(finalDir, "scripts/run.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), scriptInfo.Mode().Perm(), "scripts/run.sh exec bit must survive export -> import")

	skillMDInfo, err := destFs.Stat(filepath.Join(finalDir, "SKILL.md"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), skillMDInfo.Mode().Perm())
}

func TestImportSkillArchive_TarGzHappyPath(t *testing.T) {
	skillMD := []byte("---\nname: tarskill\ndescription: Works via tar too.\n---\n\nBody.\n")
	runSh := []byte("#!/bin/sh\necho tar\n")
	asset := []byte("asset-bytes")

	archive := buildRawTarGz(t, []rawTarEntry{
		{name: "tarskill/SKILL.md", content: skillMD, mode: 0o644},
		{name: "tarskill/scripts/run.sh", content: runSh, mode: 0o755},
		{name: "tarskill/assets/note.txt", content: asset, mode: 0o644},
	})

	fsys := afero.NewMemMapFs()
	dir, err := ImportSkillArchive(fsys, archive, "/imported", ExtractOptions{})
	require.NoError(t, err)
	assert.Equal(t, "/imported/tarskill", dir)

	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)
	assert.Equal(t, "tarskill", pkg.Frontmatter.Name)
	assert.Equal(t, "Works via tar too.", pkg.Frontmatter.Description)

	got, err := afero.ReadFile(fsys, dir+"/scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, runSh, got)

	got, err = afero.ReadFile(fsys, dir+"/assets/note.txt")
	require.NoError(t, err)
	assert.Equal(t, asset, got)

	info, err := fsys.Stat(dir + "/scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "tar exec bit must survive import")
}

func TestDetectArchiveFormat(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")
	format, err := DetectArchiveFormat(zipBytes)
	require.NoError(t, err)
	assert.Equal(t, FormatZip, format)

	tarBytes := buildRawTarGz(t, []rawTarEntry{{name: "x/SKILL.md", content: []byte("a")}})
	format, err = DetectArchiveFormat(tarBytes)
	require.NoError(t, err)
	assert.Equal(t, FormatTarGz, format)

	_, err = DetectArchiveFormat([]byte("not an archive at all"))
	require.Error(t, err)
}

// -----------------------------------------------------------------------------
// HASH VERIFY + the B2 install seam
// -----------------------------------------------------------------------------

func TestInstallSkillPackage_HashMismatchFailsLoudAndCleansUpTree(t *testing.T) {
	_, pkg, files := buildValidSkillZip(t, "humanize")

	// Build a zip with the SAME paths/modes as the signed manifest expects,
	// but tampered content for scripts/run.sh — simulating an archive that
	// arrived not matching a previously-known-good (signed) manifest.
	tampered := bytes.Repeat([]byte("EVIL"), len(files["scripts/run.sh"])/4+1)
	archive := buildRawZip(t, []rawZipEntry{
		{name: "humanize/SKILL.md", content: files["SKILL.md"], mode: 0o644},
		{name: "humanize/scripts/run.sh", content: tampered, mode: 0o755},
		{name: "humanize/assets/logo.png", content: files["assets/logo.png"], mode: 0o644},
	})

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err := InstallSkillPackage(fsys, archive, FormatZip, destDir, pkg.Manifest, nil, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity mismatch")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists, "a hash-mismatched install must be cleaned up entirely — no partial, poisoned install left behind")
}

func TestInstallSkillPackage_HappyPathVerifiesAndKeepsTree(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err := InstallSkillPackage(fsys, zipBytes, FormatZip, destDir, pkg.Manifest, nil, ExtractOptions{})
	require.NoError(t, err)

	exists, _ := afero.Exists(fsys, destDir+"/SKILL.md")
	assert.True(t, exists, "a verified install must keep the extracted tree")
}

type stubSkillSignatureVerifier struct{ err error }

func (s stubSkillSignatureVerifier) VerifyManifestSignature(SkillManifest) error { return s.err }

func TestInstallSkillPackage_SignatureVerifierGatesBeforeAnyExtraction(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")

	sentinel := errors.New("signature rejected")
	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"

	err := InstallSkillPackage(fsys, zipBytes, FormatZip, destDir, pkg.Manifest, stubSkillSignatureVerifier{err: sentinel}, ExtractOptions{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, sentinel), "the signature verifier's error must be preserved")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists, "a failed signature check must gate extraction entirely — nothing written to disk")
}

func TestNoopSkillSignatureVerifier_AcceptsEverything(t *testing.T) {
	var v SkillSignatureVerifier = NoopSkillSignatureVerifier{}
	assert.NoError(t, v.VerifyManifestSignature(nil))
	assert.NoError(t, v.VerifyManifestSignature(SkillManifest{{Path: "SKILL.md", SHA256: "sha256:x", Mode: "0644"}}))
}

// TestInstallSkillPackage_RejectedMidExtractLeavesDestDirClean is the
// regression test for the CONFIRMED defect the B2 adversarial review found:
// HardenedExtract validates and writes entries one at a time, so a hostile
// archive can get SKILL.md and an executable scripts/ payload written to disk
// before a LATER entry trips a rejection (here, a symlink — the review's own
// PoC shape: SKILL.md + scripts/backdoor.sh(0755) + a symlink entry). Before
// the atomic-install fix, InstallSkillPackage propagated HardenedExtract's
// error straight through without ever cleaning up destDir, leaving that
// partial — and executable — tree behind. The fix extracts to a staging
// directory first, so destDir is never touched unless extraction AND
// hash-verify both succeed.
func TestInstallSkillPackage_RejectedMidExtractLeavesDestDirClean(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "humanize/SKILL.md", content: []byte("---\nname: humanize\ndescription: d\n---\nbody\n"), mode: 0o644},
		{name: "humanize/scripts/backdoor.sh", content: []byte("#!/bin/sh\necho pwned\n"), mode: 0o755},
		{name: "humanize/evil-link", symlink: true},
	})
	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"

	err := InstallSkillPackage(fsys, archive, FormatZip, destDir, nil, nil, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists, "a rejected mid-extract entry must leave destDir absent entirely — no partial, poisoned tree left behind")

	backdoorExists, _ := afero.Exists(fsys, destDir+"/scripts/backdoor.sh")
	assert.False(t, backdoorExists, "the executable backdoor script written before the rejection must NOT survive at destDir — this is the confirmed defect the atomic install fixes")

	stagingExists, _ := afero.Exists(fsys, "/installed/.ctxloom-install-staging-humanize")
	assert.False(t, stagingExists, "the staging directory itself must be cleaned up too, not just destDir")
}

// =============================================================================
// PublisherSkillSignatureVerifier tests (Part B2 — the REAL signature gate)
// =============================================================================

// testSkillSigner returns a fresh ed25519 ssh.Signer/PublicKey pair.
func testSkillSigner(t *testing.T) (ssh.Signer, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)
	return signer, signer.PublicKey()
}

// skillPublisherRoot builds a real allowed_signers store trusting pub as
// principal for the publish namespace only — mirrors
// internal/signing/publisher_test.go's rootWith helper (a real store is used
// on purpose: the namespace/role check is part of what these tests exercise).
func skillPublisherRoot(principal string, pub ssh.PublicKey) *allowedsigners.Store {
	return allowedsigners.NewStore(allowedsigners.Entry{
		Principals: []string{principal},
		Namespaces: []string{signing.NamespacePublish},
		PublicKey:  pub,
	})
}

// TestPublisherSkillSignatureVerifier_SignedManifestInstallSucceeds is the
// TDD "a signed skill installs" case: a manifest signed by a key the trust
// root trusts for the publish namespace, over an archive that matches it
// exactly, must install and keep the full tree (SKILL.md and the executable
// scripts/run.sh).
func TestPublisherSkillSignatureVerifier_SignedManifestInstallSucceeds(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")
	signer, pub := testSkillSigner(t)

	sig, err := signing.Sign(pkg.Manifest.Serialize(), signer, signing.NamespacePublish)
	require.NoError(t, err)

	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: sig,
		Root:             skillPublisherRoot("bundles@ctxloom.dev", pub),
	}

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err = InstallSkillPackage(fsys, zipBytes, FormatZip, destDir, pkg.Manifest, verifier, ExtractOptions{})
	require.NoError(t, err)

	exists, _ := afero.Exists(fsys, destDir+"/SKILL.md")
	assert.True(t, exists, "a signed manifest from a trusted publisher must install successfully")
	scriptInfo, err := fsys.Stat(destDir + "/scripts/run.sh")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), scriptInfo.Mode().Perm())
}

// TestPublisherSkillSignatureVerifier_TamperedArchiveFailsAtHashGateDestDirClean
// is the TDD "tampered skill" case: the SIGNED manifest is the original,
// untampered one (exactly what a publisher signs before shipping), but the
// archive delivered at install time carries a tampered scripts/run.sh.
// Signature verification passes (it covers the untouched, signed manifest
// bytes) — but the post-extraction hash-verify recomputes the actual on-disk
// file hashes and catches the mismatch, rejecting the install and leaving
// destDir clean. Editing a file changes the manifest hash the signature
// implicitly vouches for; a signature alone can never paper over that.
func TestPublisherSkillSignatureVerifier_TamperedArchiveFailsAtHashGateDestDirClean(t *testing.T) {
	_, pkg, files := buildValidSkillZip(t, "humanize")
	signer, pub := testSkillSigner(t)
	sig, err := signing.Sign(pkg.Manifest.Serialize(), signer, signing.NamespacePublish)
	require.NoError(t, err)

	tampered := bytes.Repeat([]byte("EVIL"), len(files["scripts/run.sh"])/4+1)
	archive := buildRawZip(t, []rawZipEntry{
		{name: "humanize/SKILL.md", content: files["SKILL.md"], mode: 0o644},
		{name: "humanize/scripts/run.sh", content: tampered, mode: 0o755},
		{name: "humanize/assets/logo.png", content: files["assets/logo.png"], mode: 0o644},
	})

	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: sig,
		Root:             skillPublisherRoot("bundles@ctxloom.dev", pub),
	}

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err = InstallSkillPackage(fsys, archive, FormatZip, destDir, pkg.Manifest, verifier, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "integrity mismatch")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists, "a signed-but-tampered install must be rejected at the hash gate and leave destDir clean")
}

// TestPublisherSkillSignatureVerifier_UntrustedPublisherWithholds is the TDD
// "unsigned/untrusted-publisher skill" case: a perfectly valid signature by a
// key the trust root does NOT authorize for the publish namespace must
// withhold the install entirely — mirroring the command/prompt contract that
// an untrusted publisher's content is not trusted until a human reviews and
// accepts it (see TestSetItemTrust_ApprovesSkillCurrentVersion in
// internal/operations for that review+accept path).
func TestPublisherSkillSignatureVerifier_UntrustedPublisherWithholds(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")
	signer, _ := testSkillSigner(t)
	_, someoneElsesPub := testSkillSigner(t)

	sig, err := signing.Sign(pkg.Manifest.Serialize(), signer, signing.NamespacePublish)
	require.NoError(t, err)

	// The trust root trusts a DIFFERENT key than the one that actually signed.
	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: sig,
		Root:             skillPublisherRoot("someone-else@example.com", someoneElsesPub),
	}

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err = InstallSkillPackage(fsys, zipBytes, FormatZip, destDir, pkg.Manifest, verifier, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not by a publisher this machine trusts")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists, "an untrusted-publisher skill must not install — nothing written to disk")
}

// TestPublisherSkillSignatureVerifier_NoSignatureWithholds proves an absent
// signature is a hard install-time stop (unlike VerifyPublisher's ordinary
// "unsigned is not an error" contract, which applies to the review path, not
// this production install gate).
func TestPublisherSkillSignatureVerifier_NoSignatureWithholds(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")
	_, pub := testSkillSigner(t)

	verifier := PublisherSkillSignatureVerifier{Root: skillPublisherRoot("bundles@ctxloom.dev", pub)}

	fsys := afero.NewMemMapFs()
	destDir := "/installed/humanize"
	err := InstallSkillPackage(fsys, zipBytes, FormatZip, destDir, pkg.Manifest, verifier, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signature present")

	exists, _ := afero.Exists(fsys, destDir)
	assert.False(t, exists)
}

// TestPublisherSkillSignatureVerifier_TamperedSignatureRejected proves a
// signature that does not actually cover the given manifest (e.g. corrupted,
// or made over different bytes) is rejected outright — never silently
// downgraded to "unsigned, please review" (signing.ErrSignatureTampered's
// contract, reused unchanged here).
func TestPublisherSkillSignatureVerifier_TamperedSignatureRejected(t *testing.T) {
	_, pkg, _ := buildValidSkillZip(t, "humanize")
	signer, pub := testSkillSigner(t)

	// Sign a DIFFERENT manifest than the one actually installed.
	otherSig, err := signing.Sign([]byte("not this package's manifest bytes"), signer, signing.NamespacePublish)
	require.NoError(t, err)

	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: otherSig,
		Root:             skillPublisherRoot("bundles@ctxloom.dev", pub),
	}
	require.Error(t, verifier.VerifyManifestSignature(pkg.Manifest))
}

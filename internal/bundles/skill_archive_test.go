package bundles

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
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
// ADVERSARIAL: a symlink ALREADY PRESENT in destDir (not an archive entry)
// -----------------------------------------------------------------------------

// TestHardenedExtract_RejectsPreExistingSymlinkEscapeInDestDir is the
// regression guard for tepid-emu: safeSkillRelJoin's confinement check is
// purely LEXICAL (string path arithmetic via filepath.Join/Rel) — it has no
// way to know that one of a target path's ancestor directories might already
// be a REAL symlink on disk, planted in destDir before this call, pointing
// somewhere outside destDir entirely. The archive itself can never smuggle a
// symlink in (every symlink-typed ENTRY is rejected outright — see the tests
// above), but a hardened extractor's confinement guarantee has to hold
// regardless of what destDir already contained when it was invoked. Before
// this fix it did not: an entirely ordinary, non-symlink, non-".." archive
// entry ("subdir/evil.txt") lexically resolves to a path that LOOKS like it
// is under destDir, so the old check passed it straight through to
// afero.WriteFile — which the real OS then resolves through the symlink,
// landing the write OUTSIDE destDir. Classic directory-traversal-by-symlink.
//
// Uses a REAL OS filesystem (not MemMapFs, which cannot represent a symlink
// at all — LstatIfPossible's ok return is always false, see afero's own
// TestMemFsLstatIfPossible) so the write exercises genuine OS-level
// symlink-following semantics, not an abstraction that could never
// reproduce the bug.
func TestHardenedExtract_RejectsPreExistingSymlinkEscapeInDestDir(t *testing.T) {
	outside := t.TempDir()
	destDir := filepath.Join(t.TempDir(), "skill")
	fsys := afero.NewOsFs()
	require.NoError(t, fsys.MkdirAll(destDir, 0o755))

	// Plant the trap: destDir/subdir is a symlink pointing OUTSIDE destDir,
	// as if left behind by a previous partial extraction, a hostile
	// co-tenant of a shared staging directory, or a TOCTOU race.
	require.NoError(t, os.Symlink(outside, filepath.Join(destDir, "subdir")))

	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/subdir/evil.txt", content: []byte("pwned")},
	})

	_, err := HardenedExtract(fsys, archive, FormatZip, destDir, ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "symlink")

	// The negative assertion that matters: nothing landed OUTSIDE destDir
	// via the pre-existing symlink, even though the archive entry itself
	// carried no ".." and was never classified as a symlink entry.
	_, statErr := os.Stat(filepath.Join(outside, "evil.txt"))
	assert.True(t, os.IsNotExist(statErr), "the escape must not write outside destDir via a pre-existing symlink")
}

// TestHardenedExtract_PreExistingOrdinaryNestedDirStillWorks is the negative
// control for the fix above: a destDir that already contains ORDINARY
// (non-symlink) nested directories from a previous extraction must still
// extract normally — the symlink-escape guard must not start rejecting
// every pre-existing directory, only ones that are actually symlinks
// resolving outside destDir.
func TestHardenedExtract_PreExistingOrdinaryNestedDirStillWorks(t *testing.T) {
	destDir := filepath.Join(t.TempDir(), "skill")
	fsys := afero.NewOsFs()
	require.NoError(t, fsys.MkdirAll(filepath.Join(destDir, "subdir"), 0o755))

	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/subdir/fine.txt", content: []byte("ok")},
	})

	_, err := HardenedExtract(fsys, archive, FormatZip, destDir, ExtractOptions{})
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(destDir, "subdir", "fine.txt"))
	require.NoError(t, err)
	assert.Equal(t, "ok", string(got))
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

// TestHardenedExtract_DecompressionBombByteBoundary pins the EXACT off-by-one
// boundary in processArchiveEntry's per-entry byte budget: opts.MaxTotalBytes
// - *total + 1 sized LimitReader, checked against *total > opts.MaxTotalBytes
// afterward. The entry's content is deliberately larger than the cap in the
// rejection case so the LimitReader itself (not just the final size check)
// is what constrains the bytes actually read — pinning the INCREMENT_DECREMENT
// mutant on the "+1" headroom, not just the CONDITIONALS_BOUNDARY ">" check.
func TestHardenedExtract_DecompressionBombByteBoundary(t *testing.T) {
	const byteCap = 100

	t.Run("total uncompressed bytes exactly at the cap is ACCEPTED", func(t *testing.T) {
		content := bytes.Repeat([]byte("y"), byteCap)
		archive := buildRawZip(t, []rawZipEntry{{name: "myskill/SKILL.md", content: content}})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxTotalBytes: byteCap})
		require.NoError(t, err, "exactly cap bytes of uncompressed content must be accepted, not rejected")

		got, rerr := afero.ReadFile(fsys, "/out/skill/SKILL.md")
		require.NoError(t, rerr)
		assert.Equal(t, content, got, "the full cap-sized entry must be written intact")
	})

	t.Run("total uncompressed bytes one over the cap is REJECTED", func(t *testing.T) {
		// Content is byteCap+1 bytes: exactly one byte more than the entry
		// from the accepted case above, and large enough that the
		// LimitReader's remaining-budget size (not just the post-hoc length
		// check) is what determines the rejection.
		content := bytes.Repeat([]byte("y"), byteCap+1)
		archive := buildRawZip(t, []rawZipEntry{{name: "myskill/SKILL.md", content: content}})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxTotalBytes: byteCap})
		require.Error(t, err, "cap+1 bytes of uncompressed content must be rejected")
		assert.Contains(t, err.Error(), "decompression-bomb")

		exists, _ := afero.Exists(fsys, "/out/skill/SKILL.md")
		assert.False(t, exists, "an over-cap entry must never be committed to disk")
	})
}

// TestHardenedExtract_DecompressionBombByteBoundary_TarGz mirrors the zip
// boundary test above for extractTarGz's identical per-entry budget logic
// (processArchiveEntry is shared, but the entry-count/total bookkeeping around
// it lives separately per format).
func TestHardenedExtract_DecompressionBombByteBoundary_TarGz(t *testing.T) {
	const byteCap = 100

	t.Run("exact cap accepted", func(t *testing.T) {
		content := bytes.Repeat([]byte("z"), byteCap)
		archive := buildRawTarGz(t, []rawTarEntry{{name: "myskill/SKILL.md", content: content}})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{MaxTotalBytes: byteCap})
		require.NoError(t, err)
		got, rerr := afero.ReadFile(fsys, "/out/skill/SKILL.md")
		require.NoError(t, rerr)
		assert.Equal(t, content, got)
	})

	t.Run("cap plus one rejected", func(t *testing.T) {
		content := bytes.Repeat([]byte("z"), byteCap+1)
		archive := buildRawTarGz(t, []rawTarEntry{{name: "myskill/SKILL.md", content: content}})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{MaxTotalBytes: byteCap})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decompression-bomb")
	})
}

// countingReader wraps an io.Reader and records the largest single Read
// buffer requested and the total bytes actually yielded — used to prove
// processArchiveEntry's LimitReader is sized to the REMAINING budget
// (accounting for bytes already consumed by prior entries in the same
// archive), not the full MaxTotalBytes cap over and over per entry.
type countingReader struct {
	r     io.Reader
	total int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.total += n
	return n, err
}

// TestProcessArchiveEntry_RemainingBudgetAccountsForPriorTotal calls
// processArchiveEntry directly (white-box, same package) with a
// pre-accumulated `total` simulating bytes already consumed by earlier
// entries in the archive, and a reader with FAR more bytes available than the
// correct remaining budget — a stand-in for a real decompression bomb whose
// actual size vastly exceeds what's left of the cap. This pins that
// `remaining` is computed as `MaxTotalBytes - priorTotal + 1`, not
// `MaxTotalBytes + priorTotal + 1` or any other arithmetic on the prior
// total: a mutant flipping the subtraction would let the LimitReader read far
// more than the true remaining budget from a bomb before the entry is
// rejected, regardless of what the final accept/reject verdict ends up being.
func TestProcessArchiveEntry_RemainingBudgetAccountsForPriorTotal(t *testing.T) {
	fsys := afero.NewMemMapFs()
	var topDir string
	total := int64(30) // bytes already consumed by prior entries
	opts := ExtractOptions{MaxTotalBytes: 100, MaxEntries: 10}.normalized()

	// Correct remaining budget: 100 - 30 + 1 = 71. This entry's "content" is
	// vastly larger than that, so the LimitReader itself — not merely the
	// post-hoc size check — determines how many bytes are actually read.
	bigContent := bytes.Repeat([]byte("Q"), 10_000)
	spy := &countingReader{r: bytes.NewReader(bigContent)}

	var filesWritten int
	err := processArchiveEntry(fsys, "/out/skill", &topDir, "myskill/bomb.bin", kindFile, 0o644, spy, &total, &filesWritten, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decompression-bomb")

	assert.LessOrEqual(t, spy.total, 71,
		"the reader must never be asked to yield more than the REMAINING budget (cap - priorTotal + 1 = 71), regardless of how much content is actually available")
}

// TestHardenedExtract_ZipEntryCountBoundary pins the exact "> opts.MaxEntries"
// boundary check in extractZip (len(zr.File) > opts.MaxEntries): exactly
// MaxEntries entries must be accepted, MaxEntries+1 rejected.
func TestHardenedExtract_ZipEntryCountBoundary(t *testing.T) {
	buildEntries := func(n int) []rawZipEntry {
		entries := make([]rawZipEntry, 0, n)
		for i := 0; i < n; i++ {
			entries = append(entries, rawZipEntry{name: fmt.Sprintf("myskill/file%03d.txt", i), content: []byte("x")})
		}
		return entries
	}

	t.Run("exactly MaxEntries is accepted", func(t *testing.T) {
		archive := buildRawZip(t, buildEntries(5))
		fsys := afero.NewMemMapFs()
		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxEntries: 5})
		require.NoError(t, err, "exactly MaxEntries entries must be accepted")
	})

	t.Run("MaxEntries plus one is rejected", func(t *testing.T) {
		archive := buildRawZip(t, buildEntries(6))
		fsys := afero.NewMemMapFs()
		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{MaxEntries: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry-count bomb")
	})
}

// TestHardenedExtract_TarEntryCountBoundary mirrors the zip entry-count
// boundary test for extractTarGz's separately-maintained `count` variable.
func TestHardenedExtract_TarEntryCountBoundary(t *testing.T) {
	buildEntries := func(n int) []rawTarEntry {
		entries := make([]rawTarEntry, 0, n)
		for i := 0; i < n; i++ {
			entries = append(entries, rawTarEntry{name: fmt.Sprintf("myskill/file%03d.txt", i), content: []byte("x")})
		}
		return entries
	}

	t.Run("exactly MaxEntries is accepted", func(t *testing.T) {
		archive := buildRawTarGz(t, buildEntries(5))
		fsys := afero.NewMemMapFs()
		_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{MaxEntries: 5})
		require.NoError(t, err)
	})

	t.Run("MaxEntries plus one is rejected", func(t *testing.T) {
		archive := buildRawTarGz(t, buildEntries(6))
		fsys := afero.NewMemMapFs()
		_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{MaxEntries: 5})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry-count bomb")
	})
}

// TestParseSkillFileMode pins ExportSkillZip's octal-mode parser: valid octal
// strings parse to the expected permission bits (mask off anything above
// os.ModePerm), and a non-octal string errors loudly rather than silently
// defaulting.
func TestParseSkillFileMode(t *testing.T) {
	mode, err := parseSkillFileMode("0755")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), mode)

	mode, err = parseSkillFileMode("0644")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), mode)

	mode, err = parseSkillFileMode("0000")
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0), mode)

	_, err = parseSkillFileMode("not-octal")
	require.Error(t, err)

	_, err = parseSkillFileMode("")
	require.Error(t, err)

	_, err = parseSkillFileMode("999") // 9 is not a valid octal digit
	require.Error(t, err)
}

// TestNormalizeExtractedMode pins the exact exec-bit boundary that collapses
// every extracted mode to one of exactly two values: 0755 whenever ANY of
// owner/group/other exec bits is set (each tested individually, so a mutant
// narrowing the 0o111 mask to a single bit is caught), 0644 otherwise, and
// setuid/setgid/sticky bits are never honored regardless of the exec bits.
func TestNormalizeExtractedMode(t *testing.T) {
	tests := []struct {
		name string
		mode int64
		want os.FileMode
	}{
		{"no exec bits at all", 0o666, 0o644},
		{"read-only, no exec", 0o444, 0o644},
		{"owner exec bit only", 0o744, 0o755},
		{"group exec bit only", 0o654, 0o755},
		{"other exec bit only", 0o645, 0o755},
		{"setuid+setgid+sticky with no exec bits still normalizes to 0644", 0o7666, 0o644},
		{"setuid with an exec bit still normalizes to 0755, never honoring setuid", 0o4744, 0o755},
		{"world-writable with an exec bit still normalizes to 0755, never honoring world-write", 0o777, 0o755},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, normalizeExtractedMode(tt.mode))
		})
	}
}

// TestHardenedExtract_RejectsZipDeviceOrSocketEntry exercises zipEntryKind's
// kindOther classification (device/char/named-pipe/socket bits in the zip
// header's external attributes) — the zip-side analog of
// TestHardenedExtract_RejectsTarHardlinkAndDeviceNodes, which only exercised
// this rejection via tar. Zip cannot represent a hardlink, but it CAN carry a
// UNIX-mode socket/device bit in its external attributes, and zipEntryKind
// classifies that as kindOther exactly like tar's device typeflags do.
func TestHardenedExtract_RejectsZipDeviceOrSocketEntry(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		{name: "myskill/evil-socket", mode: os.ModeSocket | 0o644},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "special file")

	exists, _ := afero.Exists(fsys, "/out/skill/evil-socket")
	assert.False(t, exists, "no special file of any kind should ever be created by a zip archive either")
}

// TestHardenedExtract_ZipDirectoryMarkerEntry exercises zipEntryKind's kindDir
// classification via BOTH disjuncts of `mode.IsDir() || strings.HasSuffix(f.Name, "/")`
// independently: an explicit directory-mode entry, and a trailing-slash entry
// that carries no dir mode bit at all (some zip writers only set the
// trailing slash convention). Both must be treated as a directory — skipped,
// not written as a zero-byte file — and must not disturb the archive's
// single-top-level-dir bookkeeping.
func TestHardenedExtract_ZipDirectoryMarkerEntry(t *testing.T) {
	t.Run("explicit dir-mode entry is skipped as a directory marker", func(t *testing.T) {
		archive := buildRawZip(t, []rawZipEntry{
			{name: "myskill/", mode: os.ModeDir | 0o755},
			{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
		})
		fsys := afero.NewMemMapFs()

		topDir, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
		require.NoError(t, err)
		assert.Equal(t, "myskill", topDir)

		got, rerr := afero.ReadFile(fsys, "/out/skill/SKILL.md")
		require.NoError(t, rerr)
		assert.Contains(t, string(got), "myskill")
	})

	t.Run("trailing slash without the dir mode bit is still treated as a directory", func(t *testing.T) {
		archive := buildRawZip(t, []rawZipEntry{
			{name: "myskill/SKILL.md", content: []byte("---\nname: myskill\ndescription: d\n---\nbody\n")},
			{name: "myskill/emptydir/"}, // trailing slash, default (non-dir) mode from buildRawZip
		})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
		require.NoError(t, err)

		info, statErr := fsys.Stat("/out/skill/emptydir")
		require.NoError(t, statErr, "a trailing-slash entry must be created as a directory even without the dir mode bit set")
		assert.True(t, info.IsDir())
	})
}

// TestHardenedExtract_RejectsArchiveWithNoFiles (U031-F01) proves an archive
// whose every entry is single-segment (just the top-level directory marker,
// no files underneath) is REJECTED rather than reporting success having
// written zero bytes under destDir — the topDir=="" guard alone cannot catch
// this, since topDir IS set from the marker entry.
func TestHardenedExtract_RejectsArchiveWithNoFiles(t *testing.T) {
	t.Run("zip", func(t *testing.T) {
		archive := buildRawZip(t, []rawZipEntry{
			{name: "myskill/", mode: os.ModeDir | 0o755},
		})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no files")

		entries, _ := afero.ReadDir(fsys, "/out/skill")
		assert.Empty(t, entries, "nothing should be left behind by a rejected extraction")
	})

	t.Run("tar.gz", func(t *testing.T) {
		archive := buildRawTarGz(t, []rawTarEntry{
			{name: "myskill/", typeflag: tar.TypeDir},
		})
		fsys := afero.NewMemMapFs()

		_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no files")
	})
}

// TestHardenedExtract_RejectsAbsolutePathTar mirrors
// TestHardenedExtract_RejectsAbsolutePathZip for the tar code path, so
// processArchiveEntry's shared absolute-path check (path.IsAbs(slashName) ||
// filepath.IsAbs(name)) is exercised from both callers, not just zip's.
func TestHardenedExtract_RejectsAbsolutePathTar(t *testing.T) {
	archive := buildRawTarGz(t, []rawTarEntry{
		{name: "/etc/evil", content: []byte("pwned")},
	})
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, archive, FormatTarGz, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")

	exists, _ := afero.Exists(fsys, "/etc/evil")
	assert.False(t, exists)
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
	finalDir, err := ImportSkillArchive(destFs, zipBytes, "/imported", ExtractOptions{}, nil)
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
	dir, err := ImportSkillArchive(fsys, archive, "/imported", ExtractOptions{}, nil)
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

// TestDetectArchiveFormat_MagicByteBoundaries exhaustively pins every
// conditional branch of DetectArchiveFormat's magic-byte sniff: each of the
// three accepted zip local/empty/spanned-archive signatures, the gzip
// signature, and every one-byte-off negative (wrong byte 0, wrong byte 1,
// wrong bytes 2/3, too-short-for-zip, too-short-for-gzip, empty, nil). A
// mutant that inverts any single byte comparison or flips && to || in the
// zip branch's compound condition must flip at least one of these cases.
func TestDetectArchiveFormat_MagicByteBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    ArchiveFormat
		wantErr bool
	}{
		{"zip local-file-header PK\\x03\\x04", []byte{'P', 'K', 0x03, 0x04, 0x00}, FormatZip, false},
		{"zip empty-archive PK\\x05\\x06", []byte{'P', 'K', 0x05, 0x06, 0x00}, FormatZip, false},
		{"zip spanned-archive PK\\x07\\x08", []byte{'P', 'K', 0x07, 0x08, 0x00}, FormatZip, false},
		{"gzip magic \\x1f\\x8b", []byte{0x1f, 0x8b, 0x08, 0x00}, FormatTarGz, false},
		{"PK with unrecognized bytes 2/3 is unknown", []byte{'P', 'K', 0x01, 0x02}, FormatUnknown, true},
		{"wrong byte 0 is not zip", []byte{'X', 'K', 0x03, 0x04}, FormatUnknown, true},
		{"wrong byte 1 is not zip", []byte{'P', 'X', 0x03, 0x04}, FormatUnknown, true},
		{"3 bytes is too short for zip", []byte{'P', 'K', 0x03}, FormatUnknown, true},
		{"1 byte is too short for gzip", []byte{0x1f}, FormatUnknown, true},
		{"right first gzip byte, wrong second byte", []byte{0x1f, 0x00, 0x00}, FormatUnknown, true},
		{"empty data is unknown", []byte{}, FormatUnknown, true},
		{"nil data is unknown", nil, FormatUnknown, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectArchiveFormat(tt.data)
			assert.Equal(t, tt.want, got)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
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

// TestPublisherSkillSignatureVerifier_SignedManifestVerifies is the TDD "a
// signed skill verifies" case: a manifest signed by a key the trust root
// trusts for the publish namespace must verify with no error — the
// production contract operations.ImportSkill relies on
// (verifier.VerifyManifestSignature(pkg.Manifest) before it trusts the
// import).
func TestPublisherSkillSignatureVerifier_SignedManifestVerifies(t *testing.T) {
	_, pkg, _ := buildValidSkillZip(t, "humanize")
	signer, pub := testSkillSigner(t)

	sig, err := signing.Sign(pkg.Manifest.Serialize(), signer, signing.NamespacePublish)
	require.NoError(t, err)

	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: sig,
		Root:             skillPublisherRoot("bundles@ctxloom.dev", pub),
	}
	require.NoError(t, verifier.VerifyManifestSignature(pkg.Manifest))
}

// TestPublisherSkillSignatureVerifier_UntrustedPublisherWithholds is the TDD
// "unsigned/untrusted-publisher skill" case: a perfectly valid signature by a
// key the trust root does NOT authorize for the publish namespace must
// withhold — mirroring the command/prompt contract that an untrusted
// publisher's content is not trusted until a human reviews and accepts it
// (see TestSetItemTrust_ApprovesSkillCurrentVersion in internal/operations
// for that review+accept path).
func TestPublisherSkillSignatureVerifier_UntrustedPublisherWithholds(t *testing.T) {
	_, pkg, _ := buildValidSkillZip(t, "humanize")
	signer, _ := testSkillSigner(t)
	_, someoneElsesPub := testSkillSigner(t)

	sig, err := signing.Sign(pkg.Manifest.Serialize(), signer, signing.NamespacePublish)
	require.NoError(t, err)

	// The trust root trusts a DIFFERENT key than the one that actually signed.
	verifier := PublisherSkillSignatureVerifier{
		ArmoredSignature: sig,
		Root:             skillPublisherRoot("someone-else@example.com", someoneElsesPub),
	}
	err = verifier.VerifyManifestSignature(pkg.Manifest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not by a publisher this machine trusts")
}

// TestPublisherSkillSignatureVerifier_NoSignatureWithholds proves an absent
// signature is a hard stop (unlike VerifyPublisher's ordinary "unsigned is
// not an error" contract, which applies to the review path, not this
// production import gate).
func TestPublisherSkillSignatureVerifier_NoSignatureWithholds(t *testing.T) {
	_, pkg, _ := buildValidSkillZip(t, "humanize")
	_, pub := testSkillSigner(t)

	verifier := PublisherSkillSignatureVerifier{Root: skillPublisherRoot("bundles@ctxloom.dev", pub)}
	err := verifier.VerifyManifestSignature(pkg.Manifest)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no signature present")
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

// TestImportSkillArchive_FailedValidationLeavesDestinationIntact is U087-F25
// at the mechanism: the destination is computed from the ARCHIVE's own
// top-level directory name and then RemoveAll'd, so "import over the tree I
// already have" is the ordinary case. A validator that rejects the staged
// replacement must leave the existing tree byte-identical — a refusal that
// has already destroyed what it refused is the failure mode, not the error.
func TestImportSkillArchive_FailedValidationLeavesDestinationIntact(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")
	fsys := afero.NewMemMapFs()

	existing := "/imported/humanize/SKILL.md"
	require.NoError(t, fsys.MkdirAll("/imported/humanize", 0o755))
	require.NoError(t, afero.WriteFile(fsys, existing, []byte("the good tree\n"), 0o644))

	_, err := ImportSkillArchive(fsys, zipBytes, "/imported", ExtractOptions{},
		func(afero.Fs, string) error { return fmt.Errorf("nope") })
	require.Error(t, err)

	got, rerr := afero.ReadFile(fsys, existing)
	require.NoError(t, rerr, "the existing tree must survive a rejected import")
	assert.Equal(t, "the good tree\n", string(got))

	// Staging must not be left behind either.
	leftover, _ := afero.Exists(fsys, "/imported/.ctxloom-import-staging")
	assert.False(t, leftover, "staging must be cleaned up on rejection")
}

// TestImportSkillArchive_ValidatorSeesTheFinalSkillName guards the reason
// staging reproduces the final NAME: a skill package is identified by its
// directory's base name (ParseSkillPackage derives the name from it and
// matches it against the frontmatter), so a validator handed a
// staging-shaped path would fail for the wrong reason and never catch the
// real one — a gate satisfiable by the wrong evidence.
func TestImportSkillArchive_ValidatorSeesTheFinalSkillName(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")
	fsys := afero.NewMemMapFs()

	var seen string
	_, err := ImportSkillArchive(fsys, zipBytes, "/imported", ExtractOptions{},
		func(vfs afero.Fs, staged string) error {
			seen = staged
			_, perr := ParseSkillPackage(vfs, staged, 0)
			return perr
		})
	require.NoError(t, err)
	assert.Equal(t, "humanize", filepath.Base(seen),
		"the staged tree must carry the skill's real directory name")
	assert.NotEqual(t, "/imported/humanize", seen,
		"validation must run BEFORE the tree reaches its destination")
}

// =============================================================================
// U031 findings-sweep additions
// =============================================================================

// TestHardenedExtract_UnsupportedFormatLeavesNoExtractionRoot pins that a
// format rejection is a pure no-op on the filesystem. HardenedExtract used to
// create destDir before it looked at `format`, so an unsupported-format
// rejection returned an error having already made a directory the caller never
// asked for — and ImportSkillArchive computes its staging path from destDir, so
// a leftover root is exactly the debris a retry then has to reason about.
// Reject first, touch the filesystem second.
func TestHardenedExtract_UnsupportedFormatLeavesNoExtractionRoot(t *testing.T) {
	fsys := afero.NewMemMapFs()

	_, err := HardenedExtract(fsys, []byte("not an archive"), FormatUnknown, "/out/skill", ExtractOptions{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported archive format")

	exists, serr := afero.DirExists(fsys, "/out/skill")
	require.NoError(t, serr)
	assert.False(t, exists,
		"a rejected format must not leave an extraction root behind: nothing was extracted, so nothing should have been created")
}

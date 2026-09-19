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
// regression guard for a real escape: safeSkillRelJoin's confinement check is
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
	st := extractState{total: 30} // bytes already consumed by prior entries
	opts := ExtractOptions{MaxTotalBytes: 100, MaxEntries: 10}.normalized()

	// Correct remaining budget: 100 - 30 + 1 = 71. This entry's "content" is
	// vastly larger than that, so the LimitReader itself — not merely the
	// post-hoc size check — determines how many bytes are actually read.
	bigContent := bytes.Repeat([]byte("Q"), 10_000)
	spy := &countingReader{r: bytes.NewReader(bigContent)}

	err := processArchiveEntry(fsys, "/out/skill", &st, "myskill/bomb.bin", kindFile, 0o644, spy, opts)
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

// TestHardenedExtract_RejectsArchiveWithNoFiles proves an archive
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

// TestImportSkillArchive_FailedValidationLeavesDestinationIntact pins the fix
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
// Additional coverage
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

// TestExportSkillZip_EmptyManifestIsRejectedNotSilentlyPackedEmpty pins
// this project's characteristic bug, in the export path. ExportSkillZip's only
// guard was `pkg == nil`, so a SkillPackage whose Manifest is empty walked
// straight past it, the pack loop never ran, and the function returned a
// perfectly valid 22-byte end-of-central-directory-only zip with a nil error.
// Downstream (operations.ExportSkill) that lands on disk as a `<name>.zip` a
// user would reasonably believe holds their skill.
//
// Asserts the PAYLOAD, not the exit code: the failure mode being pinned IS a
// successful return, so only the bytes can tell the two apart.
func TestExportSkillZip_EmptyManifestIsRejectedNotSilentlyPackedEmpty(t *testing.T) {
	fsys := afero.NewMemMapFs()

	pkg := &SkillPackage{
		Name:        "humanize",
		Frontmatter: SkillFrontmatter{Name: "humanize", Description: "d"},
	}

	zipBytes, err := ExportSkillZip(fsys, "/src/skills/humanize", pkg)

	require.Error(t, err, "a skill package with no files must not export as a valid, empty zip")
	assert.Contains(t, err.Error(), "humanize")
	assert.Empty(t, zipBytes, "a rejected export must not hand back packable-looking bytes")
}

// TestExportSkillZip_ArchiveAlwaysCarriesEveryManifestFile is the payload half
// of the fix above: whatever ExportSkillZip returns without an error must actually
// contain one zip entry per manifest entry. A zero-entry archive returned with
// a nil error is the exact shape the guard above rejects, and this pins that a
// non-empty package still round-trips every file.
func TestExportSkillZip_ArchiveAlwaysCarriesEveryManifestFile(t *testing.T) {
	zipBytes, pkg, _ := buildValidSkillZip(t, "humanize")

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	require.NotEmpty(t, pkg.Manifest)
	assert.Len(t, zr.File, len(pkg.Manifest),
		"every manifest entry must be present as a zip entry — an archive with fewer entries than the manifest is a silent partial export")
}

// TestParseSkillFileMode_RejectsTrailingGarbageAndBasePrefixes pins the fix below.
//
// parseSkillFileMode used fmt.Sscanf(mode, "%o", &perm), which stops at the
// first byte it cannot consume and still reports success: MEASURED, Sscanf
// returns n=1, err=nil for "0755zzz", "0755 0644", "0755;rm -rf /" and
// " 0755", and — worse — for "0o755" it consumes the leading "0", reports
// success, and yields mode 0000, silently stripping the exec bit off a
// scripts/ file. SkillManifestEntry.Mode is bundle-tree data and therefore
// potentially remote-originated, so the parser must consume the WHOLE string
// or reject it. A mode string is either entirely octal digits or it is not a
// mode.
func TestParseSkillFileMode_RejectsTrailingGarbageAndBasePrefixes(t *testing.T) {
	rejected := []string{
		"0755zzz",
		"0755 0644",
		"0755;rm -rf /",
		" 0755",
		"0755\n",
		"0o755",
		"755x",
	}
	for _, in := range rejected {
		t.Run(fmt.Sprintf("rejects %q", in), func(t *testing.T) {
			_, err := parseSkillFileMode(in)
			require.Error(t, err, "a mode string that is not entirely octal digits must be rejected, not partially consumed")
			assert.Contains(t, err.Error(), "invalid manifest mode")
		})
	}

	// The accepted forms must keep working, digit-for-digit.
	accepted := map[string]os.FileMode{
		"0755":  0o755,
		"755":   0o755,
		"0644":  0o644,
		"0000":  0,
		"04755": 0o755, // setuid bit is masked off, exactly as before
	}
	for in, want := range accepted {
		t.Run(fmt.Sprintf("accepts %q", in), func(t *testing.T) {
			got, err := parseSkillFileMode(in)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

// mkdirFailFs fails MkdirAll for any path containing failOn, so a test can
// drive the two directory-creation error paths inside processArchiveEntry
// without depending on real filesystem permissions.
type mkdirFailFs struct {
	afero.Fs
	failOn string
}

func (f *mkdirFailFs) MkdirAll(p string, perm os.FileMode) error {
	if strings.Contains(filepath.ToSlash(p), f.failOn) {
		return fmt.Errorf("simulated mkdir failure")
	}
	return f.Fs.MkdirAll(p, perm)
}

// TestHardenedExtract_MkdirFailureNamesTheEntry pins the fix below. Both MkdirAll
// failures inside processArchiveEntry were returned bare — `return
// fsys.MkdirAll(target, 0o755)` and `return err` — so a mid-extraction
// directory failure surfaced as "simulated mkdir failure" with no indication of
// WHICH archive entry could not be created, while every other error on this
// path names its entry. For an adversarial-input extractor the failing entry is
// the whole diagnostic value: it is the difference between "this archive is
// hostile at path X" and "something went wrong".
func TestHardenedExtract_MkdirFailureNamesTheEntry(t *testing.T) {
	t.Run("explicit directory entry", func(t *testing.T) {
		archive := buildRawZip(t, []rawZipEntry{
			{name: "myskill/SKILL.md", content: []byte("body\n")},
			{name: "myskill/scripts/", mode: os.ModeDir | 0o755},
		})
		fsys := &mkdirFailFs{Fs: afero.NewMemMapFs(), failOn: "scripts"}

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "myskill/scripts/",
			"a directory entry that could not be created must name the entry")
	})

	t.Run("a file's parent directory", func(t *testing.T) {
		archive := buildRawZip(t, []rawZipEntry{
			{name: "myskill/scripts/run.sh", content: []byte("#!/bin/sh\n"), mode: 0o755},
		})
		fsys := &mkdirFailFs{Fs: afero.NewMemMapFs(), failOn: "scripts"}

		_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "myskill/scripts/run.sh",
			"a file whose parent directory could not be created must name the entry")
	})
}

// TestProcessArchiveEntry_UnclassifiedKindIsRejectedNotWritten pins the fix below.
//
// entryKind's zero value used to be kindFile — i.e. the UNSAFE default for a
// hardened extractor was "write these bytes to disk". Both current producers
// (zipEntryKind, tarEntryKind) return a kind explicitly, so nothing reached
// processArchiveEntry unclassified; the defect is that the type's default made
// writing the fallback rather than rejecting. A third format, or an early
// `var kind entryKind` in a future refactor, would have written an
// unclassified entry to disk in silence.
//
// The test drives entryKind's zero value on purpose. It asserts the PAYLOAD:
// before this fix the call returned nil and the file appeared on disk.
func TestProcessArchiveEntry_UnclassifiedKindIsRejectedNotWritten(t *testing.T) {
	fsys := afero.NewMemMapFs()
	var st extractState
	opts := ExtractOptions{}.normalized()

	err := processArchiveEntry(fsys, "/out/skill", &st, "myskill/payload.sh",
		entryKind(0), 0o755, bytes.NewReader([]byte("#!/bin/sh\nrm -rf /\n")), opts)

	require.Error(t, err, "entryKind's zero value must be an unclassified entry, and an unclassified entry must be rejected")
	exists, serr := afero.Exists(fsys, "/out/skill/payload.sh")
	require.NoError(t, serr)
	assert.False(t, exists, "an unclassified entry must never reach the filesystem")
	assert.Zero(t, st.filesWritten)
}

// renameFailFs fails Rename for a chosen source and/or destination path, so a
// test can make exactly one rename on the import path fail while every other
// rename still works.
type renameFailFs struct {
	afero.Fs
	failOldName string
	failNewName string
}

func (f *renameFailFs) Rename(oldname, newname string) error {
	if f.failOldName != "" && filepath.ToSlash(oldname) == f.failOldName {
		return fmt.Errorf("simulated rename failure")
	}
	if f.failNewName != "" && filepath.ToSlash(newname) == f.failNewName {
		return fmt.Errorf("simulated rename failure")
	}
	return f.Fs.Rename(oldname, newname)
}

// TestImportSkillArchive_FailedFinalRenameLeavesDestinationIntact pins the fix below.
//
// ImportSkillArchive did RemoveAll(final) and THEN Rename(staged, final). The
// window between those two calls is the whole defect: if the rename fails —
// cross-device, EACCES, a concurrent hold on the directory — the previously
// good skill tree has already been deleted and the replacement never arrives,
// so the user is left with NEITHER. The existing
// FailedValidationLeavesDestinationIntact test covers a rejection BEFORE the
// destination is touched; this covers a failure DURING the swap, which the
// staging design did not protect against.
//
// Asserts the PAYLOAD: the original file's bytes must still be readable.
func TestImportSkillArchive_FailedFinalRenameLeavesDestinationIntact(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")
	mem := afero.NewMemMapFs()

	existing := "/imported/humanize/SKILL.md"
	require.NoError(t, mem.MkdirAll("/imported/humanize", 0o755))
	require.NoError(t, afero.WriteFile(mem, existing, []byte("the good tree\n"), 0o644))

	// Fail only the staged -> final swap, the one rename the old code did
	// AFTER it had already deleted the destination.
	fsys := &renameFailFs{Fs: mem, failOldName: "/imported/.ctxloom-import-staging/humanize"}

	_, err := ImportSkillArchive(fsys, zipBytes, "/imported", ExtractOptions{}, nil)
	require.Error(t, err, "a failed swap must be reported, not swallowed")

	got, rerr := afero.ReadFile(mem, existing)
	require.NoError(t, rerr, "the previously-good tree must survive a failed swap — it was the only copy")
	assert.Equal(t, "the good tree\n", string(got),
		"a failed import must never leave the user with neither the old tree nor the new one")

	leftover, _ := afero.Exists(mem, "/imported/.ctxloom-import-staging")
	assert.False(t, leftover, "staging must be cleaned up even when the swap fails")
}

// TestImportSkillArchive_SucceedsOverAnExistingTreeAndLeavesNoBackup pins the
// other half of the fix above: the aside-and-swap must still be a clean
// REPLACEMENT on the happy path — the new tree wins, files the old tree had
// and the new one does not are gone, and no backup debris is left behind.
func TestImportSkillArchive_SucceedsOverAnExistingTreeAndLeavesNoBackup(t *testing.T) {
	zipBytes, _, files := buildValidSkillZip(t, "humanize")
	fsys := afero.NewMemMapFs()

	require.NoError(t, fsys.MkdirAll("/imported/humanize", 0o755))
	require.NoError(t, afero.WriteFile(fsys, "/imported/humanize/SKILL.md", []byte("stale\n"), 0o644))
	require.NoError(t, afero.WriteFile(fsys, "/imported/humanize/leftover.txt", []byte("gone\n"), 0o644))

	final, err := ImportSkillArchive(fsys, zipBytes, "/imported", ExtractOptions{}, nil)
	require.NoError(t, err)
	require.Equal(t, filepath.Join("/imported", "humanize"), final)

	got, rerr := afero.ReadFile(fsys, filepath.Join(final, "SKILL.md"))
	require.NoError(t, rerr)
	assert.Equal(t, string(files["SKILL.md"]), string(got), "the imported tree must replace the old one")

	stale, _ := afero.Exists(fsys, filepath.Join(final, "leftover.txt"))
	assert.False(t, stale, "a replacement import must not leave the old tree's extra files behind")

	staging, _ := afero.Exists(fsys, "/imported/.ctxloom-import-staging")
	assert.False(t, staging, "no staging or backup debris may survive a successful import")
}

// TestImportSkillArchive_UnrestorableTreeSaysWhereItIs covers the worst branch
// of the fix above: the swap failed AND the aside copy could not be put back.
// Reporting only the swap failure would send the user to look at an empty
// destination with no idea their tree is still on disk and recoverable, so the
// error must name where the only surviving copy is.
func TestImportSkillArchive_UnrestorableTreeSaysWhereItIs(t *testing.T) {
	zipBytes, _, _ := buildValidSkillZip(t, "humanize")
	mem := afero.NewMemMapFs()

	require.NoError(t, mem.MkdirAll("/imported/humanize", 0o755))
	require.NoError(t, afero.WriteFile(mem, "/imported/humanize/SKILL.md", []byte("the good tree\n"), 0o644))

	// Every rename INTO the destination fails: the swap, and then the restore.
	fsys := &renameFailFs{Fs: mem, failNewName: "/imported/humanize"}

	_, err := ImportSkillArchive(fsys, zipBytes, "/imported", ExtractOptions{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could NOT be restored")
	assert.Contains(t, err.Error(), ".replaced",
		"an unrestorable tree must be reported WITH the path it is still recoverable from")

	got, rerr := afero.ReadFile(mem, "/imported/.ctxloom-import-staging/.replaced/SKILL.md")
	require.NoError(t, rerr, "the surviving copy must not be cleaned up out from under the user")
	assert.Equal(t, "the good tree\n", string(got))
}

// -----------------------------------------------------------------------------
// Direct unit tests for the three confinement helpers
// -----------------------------------------------------------------------------
//
// tarEntryKind, symlinkEscapesRoot and resolveSymlinkChain each carry part of
// HardenedExtract's confinement guarantee, and each was previously reachable
// only through a full extraction. That is not enough for security-load-bearing
// code: an escape branch exercised only incidentally is an escape branch whose
// boundaries nobody has stated. These tests pin the classification table and
// the resolution semantics directly, so a change in either is caught here
// rather than by whichever end-to-end fixture happens to notice.
//
// The symlink tests use a REAL filesystem on purpose: afero.MemMapFs cannot
// represent a symlink at all (its LstatIfPossible always reports ok=false), so
// a MemMapFs test of these two functions would assert nothing.

// TestTarEntryKind pins tar's typeflag classification exhaustively — including
// that every typeflag the codec does not specifically recognize falls to
// kindOther (reject), never to a writable kind.
func TestTarEntryKind(t *testing.T) {
	tests := []struct {
		name     string
		typeflag byte
		want     entryKind
	}{
		{"regular file", tar.TypeReg, kindFile},
		{"directory", tar.TypeDir, kindDir},
		{"symlink", tar.TypeSymlink, kindSymlink},
		{"hardlink", tar.TypeLink, kindOther},
		{"char device", tar.TypeChar, kindOther},
		{"block device", tar.TypeBlock, kindOther},
		{"fifo", tar.TypeFifo, kindOther},
		{"pax extended header", tar.TypeXHeader, kindOther},
		{"pax global header", tar.TypeXGlobalHeader, kindOther},
		{"gnu sparse", tar.TypeGNUSparse, kindOther},
		{"gnu long name", tar.TypeGNULongName, kindOther},
		// The deprecated TypeRegA ('\x00'): Go's tar reader normalizes it to
		// TypeReg before we ever see it, so reaching this function with it
		// means something unexpected happened — fail closed.
		{"deprecated TypeRegA is not silently treated as a regular file", 0x00, kindOther},
		{"an entirely unknown typeflag", 'Q', kindOther},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tarEntryKind(&tar.Header{Typeflag: tt.typeflag}))
		})
	}
}

// osFsRoot returns a real OsFs plus a symlink-free absolute root directory, so
// filepath.Rel arithmetic in the functions under test is not confused by a
// symlinked temp dir.
func osFsRoot(t *testing.T) (afero.Fs, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return afero.NewOsFs(), root
}

func TestSymlinkEscapesRoot(t *testing.T) {
	t.Run("an ordinary nested path does not escape", func(t *testing.T) {
		fsys, root := osFsRoot(t)
		require.NoError(t, fsys.MkdirAll(filepath.Join(root, "skill", "scripts"), 0o755))

		escapes, err := symlinkEscapesRoot(fsys, root, filepath.Join(root, "skill", "scripts", "run.sh"))
		require.NoError(t, err)
		assert.False(t, escapes)
	})

	t.Run("target equal to root does not escape", func(t *testing.T) {
		fsys, root := osFsRoot(t)
		escapes, err := symlinkEscapesRoot(fsys, root, root)
		require.NoError(t, err)
		assert.False(t, escapes, "rel == \".\" is the root itself, which is confined by definition")
	})

	t.Run("a not-yet-created path does not escape", func(t *testing.T) {
		fsys, root := osFsRoot(t)
		escapes, err := symlinkEscapesRoot(fsys, root, filepath.Join(root, "nope", "deeper", "file.txt"))
		require.NoError(t, err)
		assert.False(t, escapes, "HardenedExtract is about to create these; nothing along the path can already be a symlink")
	})

	t.Run("an ancestor symlink pointing OUTSIDE root escapes", func(t *testing.T) {
		fsys, root := osFsRoot(t)
		outside := filepath.Join(root, "outside")
		inside := filepath.Join(root, "dest")
		require.NoError(t, fsys.MkdirAll(outside, 0o755))
		require.NoError(t, fsys.MkdirAll(inside, 0o755))
		linker, ok := fsys.(afero.Linker)
		require.True(t, ok)
		require.NoError(t, linker.SymlinkIfPossible(outside, filepath.Join(inside, "escape")))

		escapes, err := symlinkEscapesRoot(fsys, inside, filepath.Join(inside, "escape", "pwned"))
		require.NoError(t, err)
		assert.True(t, escapes, "a pre-existing ancestor symlink out of the extraction root must be caught")
	})

	t.Run("an ancestor symlink pointing INSIDE root does not escape", func(t *testing.T) {
		fsys, root := osFsRoot(t)
		require.NoError(t, fsys.MkdirAll(filepath.Join(root, "real"), 0o755))
		linker, ok := fsys.(afero.Linker)
		require.True(t, ok)
		require.NoError(t, linker.SymlinkIfPossible(filepath.Join(root, "real"), filepath.Join(root, "alias")))

		escapes, err := symlinkEscapesRoot(fsys, root, filepath.Join(root, "alias", "file.txt"))
		require.NoError(t, err)
		assert.False(t, escapes, "a symlink that resolves back inside the root is confined")
	})

	t.Run("a filesystem that cannot represent symlinks has nothing to check", func(t *testing.T) {
		escapes, err := symlinkEscapesRoot(afero.NewMemMapFs(), "/out", "/out/skill/file.txt")
		require.NoError(t, err)
		assert.False(t, escapes, "MemMapFs is not an afero.Lstater, so no symlink escape can exist there")
	})
}

func TestResolveSymlinkChain(t *testing.T) {
	newFs := func(t *testing.T) (afero.Fs, afero.Lstater, afero.Linker, string) {
		t.Helper()
		fsys, root := osFsRoot(t)
		lstater, ok := fsys.(afero.Lstater)
		require.True(t, ok)
		linker, ok := fsys.(afero.Linker)
		require.True(t, ok)
		return fsys, lstater, linker, root
	}

	t.Run("a regular file resolves to itself", func(t *testing.T) {
		fsys, lstater, _, root := newFs(t)
		p := filepath.Join(root, "plain.txt")
		require.NoError(t, afero.WriteFile(fsys, p, []byte("x"), 0o644))

		got, err := resolveSymlinkChain(fsys, lstater, p)
		require.NoError(t, err)
		assert.Equal(t, p, got)
	})

	t.Run("a path that does not exist resolves to itself", func(t *testing.T) {
		fsys, lstater, _, root := newFs(t)
		p := filepath.Join(root, "not-created-yet.txt")

		got, err := resolveSymlinkChain(fsys, lstater, p)
		require.NoError(t, err)
		assert.Equal(t, p, got, "a component HardenedExtract is about to create is not an error")
	})

	t.Run("a multi-hop chain resolves to its final target", func(t *testing.T) {
		fsys, lstater, linker, root := newFs(t)
		final := filepath.Join(root, "final.txt")
		require.NoError(t, afero.WriteFile(fsys, final, []byte("x"), 0o644))
		require.NoError(t, linker.SymlinkIfPossible(final, filepath.Join(root, "hop2")))
		require.NoError(t, linker.SymlinkIfPossible(filepath.Join(root, "hop2"), filepath.Join(root, "hop1")))

		got, err := resolveSymlinkChain(fsys, lstater, filepath.Join(root, "hop1"))
		require.NoError(t, err)
		assert.Equal(t, final, got, "every hop must be followed, not just the first")
	})

	t.Run("a relative link target resolves against the symlink's own directory", func(t *testing.T) {
		fsys, lstater, linker, root := newFs(t)
		require.NoError(t, fsys.MkdirAll(filepath.Join(root, "sub"), 0o755))
		target := filepath.Join(root, "sub", "target.txt")
		require.NoError(t, afero.WriteFile(fsys, target, []byte("x"), 0o644))
		// "target.txt" is relative: it must resolve inside root/sub, NOT
		// against the process working directory.
		require.NoError(t, linker.SymlinkIfPossible("target.txt", filepath.Join(root, "sub", "link")))

		got, err := resolveSymlinkChain(fsys, lstater, filepath.Join(root, "sub", "link"))
		require.NoError(t, err)
		assert.Equal(t, target, got)
	})

	t.Run("a symlink loop is bounded, not hung", func(t *testing.T) {
		_, lstater, linker, root := newFs(t)
		fsys := afero.NewOsFs()
		a := filepath.Join(root, "a")
		b := filepath.Join(root, "b")
		require.NoError(t, linker.SymlinkIfPossible(b, a))
		require.NoError(t, linker.SymlinkIfPossible(a, b))

		_, err := resolveSymlinkChain(fsys, lstater, a)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "symlink chain too deep",
			"a planted loop must be rejected by the depth cap, mirroring the OS's own ELOOP")
	})
}

// -----------------------------------------------------------------------------
// Characterization tests for processArchiveEntry's remaining arms
// -----------------------------------------------------------------------------
//
// processArchiveEntry sits at CCN 23
// against this project's own CCN-10 gate, with ten parameters, two of them
// mutable accumulators threaded through both format loops. Behaviour is
// unchanged by definition, so no test can discriminate before from after — these
// are CHARACTERIZATION tests that must be green on both sides. The arms already covered elsewhere in this file (traversal,
// absolute paths, symlinks, hardlinks/devices, a second top-level directory,
// the byte and entry caps, the directory marker, a pre-existing symlink escape,
// an unclassified kind, and both MkdirAll failures) are not duplicated here;
// these three are the ones nothing reached.

// lstatErrFs is an afero.Lstater whose LstatIfPossible fails for a chosen path,
// so the symlink-escape CHECK's own error path can be exercised — distinct from
// the check reporting an escape.
type lstatErrFs struct {
	afero.Fs
	failOn string
}

func (f *lstatErrFs) LstatIfPossible(name string) (os.FileInfo, bool, error) {
	if strings.Contains(filepath.ToSlash(name), f.failOn) {
		return nil, false, fmt.Errorf("simulated lstat failure")
	}
	fi, err := f.Stat(name)
	return fi, false, err
}

func TestProcessArchiveEntry_EmptyEntryNameIsRejected(t *testing.T) {
	fsys := afero.NewMemMapFs()
	var st extractState

	err := processArchiveEntry(fsys, "/out/skill", &st, "", kindFile, 0o644,
		bytes.NewReader([]byte("x")), ExtractOptions{}.normalized())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty name")
	assert.Zero(t, st.filesWritten)
}

func TestProcessArchiveEntry_NameCleaningToDotIsRejected(t *testing.T) {
	fsys := afero.NewMemMapFs()
	var st extractState

	// "./" cleans to "." — a name that addresses the extraction root itself
	// rather than anything inside it.
	err := processArchiveEntry(fsys, "/out/skill", &st, "./", kindDir, 0o755,
		bytes.NewReader(nil), ExtractOptions{}.normalized())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolves to an empty path")
	assert.Empty(t, st.topDir, "a rejected name must not become the archive's top-level directory")
}

func TestHardenedExtract_SymlinkCheckFailureIsReportedNotIgnored(t *testing.T) {
	archive := buildRawZip(t, []rawZipEntry{
		{name: "myskill/SKILL.md", content: []byte("body\n")},
	})
	fsys := &lstatErrFs{Fs: afero.NewMemMapFs(), failOn: "SKILL.md"}

	_, err := HardenedExtract(fsys, archive, FormatZip, "/out/skill", ExtractOptions{})
	require.Error(t, err, "a confinement check that cannot run must fail closed, never be treated as 'no escape'")
	assert.Contains(t, err.Error(), "checking for a pre-existing symlink escape")
	assert.Contains(t, err.Error(), "myskill/SKILL.md")

	written, serr := afero.Exists(fsys, "/out/skill/SKILL.md")
	require.NoError(t, serr)
	assert.False(t, written, "nothing may be written when confinement could not be established")
}

// =============================================================================
// Mode disagreement is a DECLARATION problem, never tampering
// =============================================================================

// verifyFixtureManifest builds the on-disk manifest for a fixture package and
// returns it with one entry's mode overridden — the shape of a package whose
// `executable:` declaration and whose tree disagree.
func verifyFixtureManifest(t *testing.T, fsys afero.Fs, dir, path, mode string) SkillManifest {
	t.Helper()
	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)
	out := make(SkillManifest, 0, len(pkg.Manifest))
	for _, e := range pkg.Manifest {
		if e.Path == path {
			e.Mode = mode
		}
		out = append(out, e)
	}
	return out
}

// TestVerifyExtractedManifest_UndeclaredExecutableSaysDeclaredNotTampered is
// the missing-YAML-line case: the author chmod +x'd scripts/run.sh and never
// added it to the sidecar's executable: list.
//
// The old wording ("mode 0755 does not match the signed manifest mode 0644 —
// rejected (integrity mismatch)") sent them looking for an attacker. Nothing
// was tampered with: a mode bit is not in the digest and no signature covers
// it, so the two sides of this comparison are a declaration and a filesystem.
func TestVerifyExtractedManifest_UndeclaredExecutableSaysDeclaredNotTampered(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	writeSkillFixture(t, fsys, dir, "humanize")

	err := VerifyExtractedManifest(fsys, dir, verifyFixtureManifest(t, fsys, dir, "scripts/run.sh", "0644"))

	require.Error(t, err, "a declaration that disagrees with the tree must still refuse the package")
	assert.Contains(t, err.Error(), "DECLARED non-executable",
		"the message has to name the declaration, which is the thing the author must change")
	assert.Contains(t, err.Error(), "executable:",
		"and the line to add")
	assert.Contains(t, err.Error(), "scripts/run.sh")
	assert.NotContains(t, err.Error(), "integrity mismatch",
		"a mode is not attested, so it can never be evidence of tampering")
}

// TestVerifyExtractedManifest_DeclaredExecutableThatLostItsBitSaysSo is the
// other direction: the declaration is right and the exec bit went missing (a
// checkout on a filesystem without one, an archive that dropped it). Same
// non-accusatory framing, opposite remedy.
func TestVerifyExtractedManifest_DeclaredExecutableThatLostItsBitSaysSo(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	writeSkillFixture(t, fsys, dir, "humanize")

	err := VerifyExtractedManifest(fsys, dir, verifyFixtureManifest(t, fsys, dir, "assets/logo.png", "0755"))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "DECLARED executable")
	assert.Contains(t, err.Error(), "assets/logo.png")
	assert.NotContains(t, err.Error(), "integrity mismatch")
}

// TestVerifyExtractedManifest_ContentAndFileListStillReportIntegrity pins the
// other half: relaxing the MODE wording must not soften the two comparisons a
// signature genuinely attests.
func TestVerifyExtractedManifest_ContentAndFileListStillReportIntegrity(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	writeSkillFixture(t, fsys, dir, "humanize")
	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)

	tampered := make(SkillManifest, 0, len(pkg.Manifest))
	for _, e := range pkg.Manifest {
		if e.Path == "scripts/run.sh" {
			e.SHA256 = "sha256:" + strings.Repeat("0", 64)
		}
		tampered = append(tampered, e)
	}

	verr := VerifyExtractedManifest(fsys, dir, tampered)
	require.Error(t, verr)
	assert.Contains(t, verr.Error(), "integrity mismatch",
		"a content hash IS attested; a difference there is exactly what tampering looks like")
}

// =============================================================================
// Only the EXEC BIT is compared — the rest of a mode does not round-trip
// =============================================================================

// declaredThenChmod writes the standard fixture, captures the manifest as it
// reads BEFORE any chmod (that is the DECLARATION side — 0644/0755, what an
// author writes and what a signature covers), then re-modes the files on disk
// to whatever the caller asks for (that is the FILESYSTEM side). It returns
// the declaration.
//
// The two sides are captured in that order deliberately: it is the only way to
// build a package whose declared and on-disk modes genuinely differ without
// hand-writing mode strings that could drift from what SkillManifestEntryFor
// actually emits.
func declaredThenChmod(t *testing.T, fsys afero.Fs, dir string, onDisk map[string]os.FileMode) SkillManifest {
	t.Helper()
	writeSkillFixture(t, fsys, dir, "humanize")
	pkg, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)
	declared := pkg.Manifest

	for rel, mode := range onDisk {
		require.NoError(t, fsys.Chmod(filepath.Join(dir, filepath.FromSlash(rel)), mode))
	}

	// Guard against a vacuous test: if the chmod did not actually land (an
	// afero backend that ignores modes, say), "declared and on-disk differ"
	// would be false and every assertion below would pass for the wrong
	// reason. Require a real, observable difference first.
	after, err := ParseSkillPackage(fsys, dir, 0)
	require.NoError(t, err)
	byPath := map[string]string{}
	for _, e := range after.Manifest {
		byPath[e.Path] = e.Mode
	}
	changed := 0
	for _, e := range declared {
		if byPath[e.Path] != e.Mode {
			changed++
		}
	}
	require.NotZero(t, changed,
		"fixture setup produced no on-disk mode difference at all — the comparison under test would be trivially satisfied")
	return declared
}

// TestVerifyExtractedManifest_NonExecModeBitsAreNotADisagreement is the
// release-blocking case: a fresh clone under umask 002 lands every file at
// 0664/0775, the package declares 0644/0755, and the full-mode compare
// withheld content that verifies.
//
// There is no repair for it in the repository either: `chmod 0644` followed by
// `git add` stages NOTHING, because git records the exec bit and nothing else,
// so the tree can never be made to agree with a 0644 declaration for the next
// person who clones. The group/other bits are umask, filesystem, and platform
// noise; they carry no meaning about the package and are not compared.
func TestVerifyExtractedManifest_NonExecModeBitsAreNotADisagreement(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	declared := declaredThenChmod(t, fsys, dir, map[string]os.FileMode{
		"SKILL.md":        0o664,
		"scripts/run.sh":  0o775,
		"assets/logo.png": 0o664,
	})

	err := VerifyExtractedManifest(fsys, dir, declared)

	require.NoError(t, err,
		"a mode difference confined to the group/other write bits is umask noise, not a declaration the author got wrong")
}

// TestVerifyExtractedManifest_ExecBitDifferenceIsStillRefused is the half that
// stops the fix degenerating into "ignore mode entirely". The exec bit is the
// one part of a mode that IS semantically the skill's content — it decides
// whether a scripts/ file runs — and it is the one part git round-trips, so a
// disagreement there is both meaningful and repairable.
//
// Both directions are covered, and both are stated in umask-shaped modes
// (0664/0775) rather than a bare 0644/0755, because those are the modes a real
// clone produces and an exec-bit compare has to reach the right message for
// them too.
func TestVerifyExtractedManifest_ExecBitDifferenceIsStillRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		onDisk   os.FileMode
		wantSaid string
		wantFix  string
	}{
		{
			name:     "declared non-executable, executable on disk",
			path:     "assets/logo.png", // fixture declares 0644
			onDisk:   0o775,
			wantSaid: "DECLARED non-executable",
			wantFix:  "executable:",
		},
		{
			name:     "declared executable, non-executable on disk",
			path:     "scripts/run.sh", // fixture declares 0755
			onDisk:   0o664,
			wantSaid: "DECLARED executable",
			wantFix:  "the exec bit was lost",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := afero.NewMemMapFs()
			dir := "/bundle/skills/humanize"
			declared := declaredThenChmod(t, fsys, dir, map[string]os.FileMode{tc.path: tc.onDisk})

			err := VerifyExtractedManifest(fsys, dir, declared)

			require.Error(t, err,
				"an exec-bit disagreement changes whether the file RUNS; it must still withhold the package")
			assert.Contains(t, err.Error(), tc.wantSaid)
			assert.Contains(t, err.Error(), tc.wantFix)
			assert.Contains(t, err.Error(), tc.path)
			assert.Contains(t, err.Error(), "NOT tampering",
				"the withholding message stays non-accusatory: no signature covers a mode bit")
			assert.NotContains(t, err.Error(), "integrity mismatch")
		})
	}
}

// TestVerifyExtractedManifest_LooseModeDoesNotLoosenContent pins the inverse
// defect: relaxing the mode compare must not let two genuinely different files
// through. The modes here differ only in umask noise (so the mode check is
// satisfied and cannot be what refuses the package) while the content differs
// — and content is what a signature actually attests.
func TestVerifyExtractedManifest_LooseModeDoesNotLoosenContent(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	declared := declaredThenChmod(t, fsys, dir, map[string]os.FileMode{
		"scripts/run.sh": 0o775,
	})
	require.NoError(t, afero.WriteFile(fsys, dir+"/scripts/run.sh", []byte("#!/bin/sh\nrm -rf /\n"), 0o775))

	err := VerifyExtractedManifest(fsys, dir, declared)

	require.Error(t, err, "a swapped file body must be refused however permissive the mode compare has become")
	assert.Contains(t, err.Error(), "integrity mismatch",
		"a content hash IS attested; a difference there is exactly what tampering looks like")
	assert.Contains(t, err.Error(), "scripts/run.sh")
}

// TestVerifyExtractedManifest_EmptyManifestIsNotAVerifiedPackage guards the
// vacuous case: len(actual) == len(expected) == 0 satisfies every comparison in
// the loop by never entering it, and an empty skill materializes without
// complaint and delivers nothing — ctxloom's characteristic silent no-op.
func TestVerifyExtractedManifest_EmptyManifestIsNotAVerifiedPackage(t *testing.T) {
	fsys := afero.NewMemMapFs()
	dir := "/bundle/skills/humanize"
	require.NoError(t, fsys.MkdirAll(dir, 0o755))

	err := VerifyExtractedManifest(fsys, dir, SkillManifest{})

	require.Error(t, err, "two empty manifests match trivially; that is not a package that verified")
	assert.Contains(t, err.Error(), "NO files")
}

// TestSkillModeExecutable_UnparseableModeIsAnErrorNotFalse pins the fail-loud
// half of the predicate. A mode string that does not parse must never quietly
// answer "not executable": that answer would make a package declaring garbage
// agree with a plain file on disk and verify.
func TestSkillModeExecutable_UnparseableModeIsAnErrorNotFalse(t *testing.T) {
	for _, mode := range []string{"", "rwxr-xr-x", "0o755", "8888", "-1"} {
		t.Run(mode, func(t *testing.T) {
			exec, err := skillModeExecutable(mode)
			require.Error(t, err, "an unreadable mode is a refusal, never a silent 'non-executable'")
			assert.False(t, exec)
			assert.Contains(t, err.Error(), mode)
		})
	}

	for _, tc := range []struct {
		mode string
		want bool
	}{
		{"0644", false}, {"0664", false}, {"0600", false},
		{"0755", true}, {"0775", true}, {"0700", true}, {"0744", true},
	} {
		exec, err := skillModeExecutable(tc.mode)
		require.NoError(t, err)
		assert.Equal(t, tc.want, exec, "mode %s", tc.mode)
	}
}

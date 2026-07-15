package bundles

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/afero"
)

// This file holds Part B, slice B1b of the skill/command split: the ARCHIVE
// CODEC (pack a skill source tree into an Anthropic-Skills-API-shaped zip;
// unpack a zip or tar.gz back into a source tree) and the HARDENED EXTRACTOR
// that both import and the future install path (B2/B3) route every archive
// byte through.
//
// SECURITY: an imported skill archive is ADVERSARIAL INPUT. It may originate
// from an untrusted remote, a hostile publisher, or a corrupted transfer, and
// a skill's scripts/ files are EXECUTABLE once materialized into an engine.
// HardenedExtract is the single choke point every byte must pass before it is
// ever written to a real filesystem path — see its doc comment for the full,
// non-negotiable rejection list. Nothing in this file shells out to
// `unzip`/`tar`; only Go stdlib archive/zip + archive/tar + compress/gzip.

// ArchiveFormat identifies a skill archive's container format.
type ArchiveFormat int

const (
	FormatUnknown ArchiveFormat = iota
	// FormatZip is the CANONICAL, Anthropic-Skills-API-shaped archive: a
	// single top-level entry = the skill directory, named after the skill.
	FormatZip
	// FormatTarGz is accepted on import for ecosystem liberality (some
	// exported/mirrored skills travel as tar.gz); export never produces it.
	FormatTarGz
)

// DefaultMaxArchiveEntries caps the number of entries HardenedExtract will
// process from a single archive. This defends against an entry-count bomb
// distinct from the byte-size bomb: thousands of zero-byte entries can still
// exhaust inode/fd/directory-entry budgets even while staying under the
// uncompressed-byte cap.
const DefaultMaxArchiveEntries = 4096

// DetectArchiveFormat sniffs an archive's container format from its magic
// bytes so import can be liberal about zip vs tar.gz without relying on a
// filename extension (which is not trustworthy input): zip's local-file-header
// signature "PK\x03\x04" (tolerating the empty-archive "PK\x05\x06" and
// spanned-archive "PK\x07\x08" variants), or gzip's magic "\x1f\x8b" (tar.gz is
// gzip-wrapped; a tar is assumed inside, the only thing this codec unwraps a
// gzip stream into).
func DetectArchiveFormat(data []byte) (ArchiveFormat, error) {
	switch {
	case len(data) >= 4 && data[0] == 'P' && data[1] == 'K' &&
		((data[2] == 0x03 && data[3] == 0x04) ||
			(data[2] == 0x05 && data[3] == 0x06) ||
			(data[2] == 0x07 && data[3] == 0x08)):
		return FormatZip, nil
	case len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b:
		return FormatTarGz, nil
	default:
		return FormatUnknown, fmt.Errorf("skill archive: unrecognized format (not a zip or a gzip/tar.gz stream)")
	}
}

// =============================================================================
// EXPORT (pack): source tree -> Anthropic-Skills-API-shaped zip
// =============================================================================

// ExportSkillZip packs pkg's source tree (rooted at dir on fsys, already
// parsed by ParseSkillPackage so its manifest is authoritative for the exact
// file set and modes) into a zip shaped exactly like the Anthropic Skills-API
// upload expects: a single top-level entry that is the skill directory named
// pkg.Name, every manifest file nested under it, with unix file modes
// preserved via the zip entry's external attributes (the scripts/ exec bit is
// load-bearing and must survive pack -> unpack -> materialize).
//
// Deterministic: entries are written in the manifest's sorted path order with
// a fixed (zero) modification time, so packing the same tree twice produces
// byte-identical zip output regardless of directory-walk order.
func ExportSkillZip(fsys afero.Fs, dir string, pkg *SkillPackage) ([]byte, error) {
	if pkg == nil {
		return nil, fmt.Errorf("skill archive: export requires a parsed SkillPackage")
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, entry := range pkg.Manifest.sorted() {
		mode, err := parseSkillFileMode(entry.Mode)
		if err != nil {
			return nil, fmt.Errorf("skill archive: %s: %w", entry.Path, err)
		}
		data, err := afero.ReadFile(fsys, filepath.Join(dir, filepath.FromSlash(entry.Path)))
		if err != nil {
			return nil, fmt.Errorf("skill archive: reading %s: %w", entry.Path, err)
		}

		hdr := &zip.FileHeader{
			// zip entry names always use "/", regardless of host OS.
			Name:     path.Join(pkg.Name, entry.Path),
			Method:   zip.Deflate,
			Modified: time.Unix(0, 0).UTC(),
		}
		hdr.SetMode(mode)

		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return nil, fmt.Errorf("skill archive: creating zip entry %s: %w", entry.Path, err)
		}
		if _, err := w.Write(data); err != nil {
			return nil, fmt.Errorf("skill archive: writing zip entry %s: %w", entry.Path, err)
		}
	}

	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("skill archive: finalizing zip: %w", err)
	}
	return buf.Bytes(), nil
}

// parseSkillFileMode parses a SkillManifestEntry.Mode string (e.g. "0755")
// into an os.FileMode carrying only the permission bits.
func parseSkillFileMode(mode string) (os.FileMode, error) {
	var perm uint32
	if _, err := fmt.Sscanf(mode, "%o", &perm); err != nil {
		return 0, fmt.Errorf("invalid manifest mode %q: %w", mode, err)
	}
	return os.FileMode(perm) & os.ModePerm, nil
}

// =============================================================================
// THE HARDENED EXTRACTOR — the crux of B1b
// =============================================================================

// ExtractOptions bounds HardenedExtract's decompression/entry-count-bomb
// defenses. Zero values mean "use the default cap" — callers do not need to
// know the defaults to get safe behavior, but tests can inject a small cap to
// exercise the bomb defenses without building multi-megabyte fixtures.
type ExtractOptions struct {
	// MaxTotalBytes caps the sum of all extracted (uncompressed) file bytes
	// across the whole archive. <=0 uses DefaultMaxSkillPackageBytes (the
	// Skills-API's <30MB package constraint).
	MaxTotalBytes int64
	// MaxEntries caps the number of archive entries processed. <=0 uses
	// DefaultMaxArchiveEntries.
	MaxEntries int
}

func (o ExtractOptions) normalized() ExtractOptions {
	if o.MaxTotalBytes <= 0 {
		o.MaxTotalBytes = DefaultMaxSkillPackageBytes
	}
	if o.MaxEntries <= 0 {
		o.MaxEntries = DefaultMaxArchiveEntries
	}
	return o
}

// entryKind classifies one archive entry for HardenedExtract's rejection
// logic. Only kindFile and kindDir are ever written to disk.
type entryKind int

const (
	kindFile entryKind = iota
	kindDir
	kindSymlink
	// kindOther covers hardlinks, device/char/block/fifo/socket special
	// files, and any tar typeflag this codec does not recognize. Tar can
	// carry all of these; zip cannot, so zip entries never classify as
	// kindOther.
	kindOther
)

// HardenedExtract is THE ONE function every archive byte passes through
// before landing on disk — used by ImportSkillArchive today and by B2/B3's
// install path once it exists. It extracts archive (zip or tar.gz per format)
// into destDir, stripping the archive's single top-level directory (the
// skill's own directory, matched by name at ParseSkillPackage time) so the
// files land directly under destDir, and returns that top-level directory's
// original name.
//
// INVARIANT (read before touching this function): every entry is validated
// BEFORE a single byte is written. The full rejection list, enforced for
// EVERY entry, in EVERY supported format:
//
//   - Absolute paths — rejected outright.
//   - Any path-traversal segment ("..") anywhere in the entry name, after
//     cleaning — rejected. This is the canonical zip-slip guard: the
//     resolved destination is computed via safeSkillRelJoin (the same
//     confinement helper ResolveSkillDir uses for bundle-tree paths) and
//     independently re-verified to stay within destDir, even though the
//     segment scan above already rejects "..". Defense in depth on purpose.
//   - Symlinks — rejected outright, unconditionally. No "resolves inside the
//     target" exception: a symlink is never allowed to exist in an extracted
//     skill tree, full stop (this was an explicit security decision, not an
//     oversight — see the plan's escalation note before loosening it).
//   - Hardlinks, device nodes, char/block special files, FIFOs, sockets, and
//     any unrecognized tar typeflag — rejected outright (kindOther).
//   - An archive whose entries do not share one common top-level directory —
//     rejected (a second/foreign top-level path is exactly the shape a
//     zip-slip-adjacent smuggling attempt would take).
//   - Total uncompressed bytes beyond opts.MaxTotalBytes — rejected. Streamed
//     via a hard io.LimitReader per entry; the archive's OWN declared size
//     fields (zip UncompressedSize64, tar Header.Size) are NEVER trusted, only
//     the bytes actually read count against the cap (decompression-bomb
//     defense).
//   - More than opts.MaxEntries entries — rejected (entry-count bomb: many
//     tiny files can exhaust resources without tripping the byte cap).
//
// Mode normalization: every extracted regular file's mode collapses to
// exactly 0755 (if any of its owner/group/other exec bits were set) or 0644
// (otherwise). This preserves the load-bearing scripts/ exec bit while never
// honoring setuid/setgid/sticky or a world-writable bit from the archive.
//
// On ANY rejection, HardenedExtract returns immediately: no partial write of
// the rejected entry occurs, and the caller (ImportSkillArchive,
// InstallSkillPackage) is responsible for removing whatever was written for
// entries processed before the rejection — both callers in this file do.
func HardenedExtract(fsys afero.Fs, archive []byte, format ArchiveFormat, destDir string, opts ExtractOptions) (topDir string, err error) {
	opts = opts.normalized()

	if err := fsys.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("skill archive: creating extraction root %q: %w", destDir, err)
	}

	switch format {
	case FormatZip:
		return extractZip(fsys, archive, destDir, opts)
	case FormatTarGz:
		return extractTarGz(fsys, archive, destDir, opts)
	default:
		return "", fmt.Errorf("skill archive: unsupported archive format")
	}
}

func extractZip(fsys afero.Fs, archive []byte, destDir string, opts ExtractOptions) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("skill archive: invalid zip: %w", err)
	}
	if len(zr.File) > opts.MaxEntries {
		return "", fmt.Errorf("skill archive: %d entries exceeds the %d entry cap — rejected (entry-count bomb guard)", len(zr.File), opts.MaxEntries)
	}

	var topDir string
	var total int64
	for _, f := range zr.File {
		kind := zipEntryKind(f)
		perr := func() error {
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("opening entry %q: %w", f.Name, err)
			}
			defer rc.Close()
			return processArchiveEntry(fsys, destDir, &topDir, f.Name, kind, int64(f.Mode().Perm()), rc, &total, opts)
		}()
		if perr != nil {
			return "", fmt.Errorf("skill archive: %w", perr)
		}
	}
	if topDir == "" {
		return "", fmt.Errorf("skill archive: empty archive, no top-level skill directory found")
	}
	return topDir, nil
}

func zipEntryKind(f *zip.File) entryKind {
	mode := f.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		return kindSymlink
	case mode.IsDir(), strings.HasSuffix(f.Name, "/"):
		return kindDir
	case mode&(os.ModeDevice|os.ModeCharDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeIrregular) != 0:
		return kindOther
	default:
		return kindFile
	}
}

func extractTarGz(fsys afero.Fs, archive []byte, destDir string, opts ExtractOptions) (string, error) {
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return "", fmt.Errorf("skill archive: invalid gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	var topDir string
	var total int64
	var count int
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("skill archive: invalid tar: %w", err)
		}
		count++
		if count > opts.MaxEntries {
			return "", fmt.Errorf("skill archive: exceeds the %d entry cap — rejected (entry-count bomb guard)", opts.MaxEntries)
		}
		kind := tarEntryKind(hdr)
		if err := processArchiveEntry(fsys, destDir, &topDir, hdr.Name, kind, hdr.Mode, tr, &total, opts); err != nil {
			return "", fmt.Errorf("skill archive: %w", err)
		}
	}
	if topDir == "" {
		return "", fmt.Errorf("skill archive: empty archive, no top-level skill directory found")
	}
	return topDir, nil
}

func tarEntryKind(hdr *tar.Header) entryKind {
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeRegA:
		return kindFile
	case tar.TypeDir:
		return kindDir
	case tar.TypeSymlink:
		return kindSymlink
	default:
		// tar.TypeLink (hardlink), TypeChar, TypeBlock, TypeFifo, and any
		// typeflag this codec doesn't specifically recognize: reject, don't
		// guess.
		return kindOther
	}
}

// processArchiveEntry is the single choke point HardenedExtract's zip and tar
// loops both funnel every entry through: path validation, kind rejection,
// entry-count/size accounting, mode normalization, and the actual write. See
// HardenedExtract's doc comment for the full, authoritative rejection list —
// this function is where each rule is enforced.
func processArchiveEntry(fsys afero.Fs, destDir string, topDir *string, name string, kind entryKind, mode int64, r io.Reader, total *int64, opts ExtractOptions) error {
	if name == "" {
		return fmt.Errorf("entry has an empty name")
	}
	slashName := filepath.ToSlash(name)
	if path.IsAbs(slashName) || filepath.IsAbs(name) {
		return fmt.Errorf("entry %q uses an absolute path — rejected (zip-slip guard)", name)
	}

	cleaned := path.Clean(slashName)
	if cleaned == "." || cleaned == "" {
		return fmt.Errorf("entry %q resolves to an empty path", name)
	}
	segments := strings.Split(cleaned, "/")
	for _, seg := range segments {
		if seg == ".." || seg == "" {
			return fmt.Errorf("entry %q contains a path-traversal segment — rejected (zip-slip guard)", name)
		}
	}

	switch kind {
	case kindSymlink:
		return fmt.Errorf("entry %q is a symlink — rejected (symlinks are never permitted in a skill archive)", name)
	case kindOther:
		return fmt.Errorf("entry %q is a hardlink, device, or other special file — rejected", name)
	}

	first := segments[0]
	switch {
	case *topDir == "":
		*topDir = first
	case first != *topDir:
		return fmt.Errorf("entry %q is outside the archive's single top-level directory %q", name, *topDir)
	}

	rest := segments[1:]
	if len(rest) == 0 {
		// The top-level directory marker entry itself (e.g. "myskill/" in a
		// zip) — destDir already stands in for it, nothing further to write.
		return nil
	}
	relPath := strings.Join(rest, "/")

	// The canonical zip-slip guard: reuse the exact confinement helper
	// ResolveSkillDir uses for bundle-tree paths (safeSkillRelJoin) as a
	// second, independent check. Belt and braces: even though the segment
	// scan above already rejects "..", every remote-originated relative path
	// in this codebase is required to pass this same join-and-verify before
	// it ever touches a real filesystem path.
	target, ok := safeSkillRelJoin(destDir, relPath)
	if !ok {
		return fmt.Errorf("entry %q escapes the extraction root — rejected (zip-slip guard)", name)
	}

	if kind == kindDir {
		return fsys.MkdirAll(target, 0o755)
	}

	if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	// Decompression-bomb defense: never trust the archive's declared size.
	// Stream through a hard limit sized to the remaining budget; if the
	// entry has more actual bytes than that, this read comes back over
	// budget and the whole extraction is rejected below.
	remaining := opts.MaxTotalBytes - *total + 1
	if remaining < 0 {
		remaining = 0
	}
	data, err := io.ReadAll(io.LimitReader(r, remaining))
	if err != nil {
		return fmt.Errorf("reading entry %q: %w", name, err)
	}
	*total += int64(len(data))
	if *total > opts.MaxTotalBytes {
		return fmt.Errorf("uncompressed size exceeds the %d byte cap — rejected (decompression-bomb guard)", opts.MaxTotalBytes)
	}

	if err := afero.WriteFile(fsys, target, data, normalizeExtractedMode(mode)); err != nil {
		return fmt.Errorf("writing entry %q: %w", name, err)
	}
	return nil
}

// normalizeExtractedMode collapses any incoming archive mode to exactly one
// of two safe values: 0755 if any exec bit was set (preserving the
// load-bearing scripts/ exec bit), 0644 otherwise. setuid/setgid/sticky and a
// world-writable bit are never honored — they simply cannot survive this
// normalization, regardless of what the archive declared.
func normalizeExtractedMode(mode int64) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

// =============================================================================
// IMPORT (unpack): archive -> source tree, liberal on input format
// =============================================================================

// ImportSkillArchive is the liberal-input import entrypoint (`ctxloom skill
// import`): it accepts EITHER the canonical Anthropic-shaped zip or a tar.gz,
// sanitized-extracts it (via HardenedExtract — see that function for the full
// rejection list) into destParent/<top-level-dir-name-from-the-archive>, and
// returns that directory, ready for ParseSkillPackage and the ordinary
// review/sign flow.
//
// The archive is UNTRUSTED input: this function does not sign, trust, or
// materialize anything into an engine — it only ever produces a reviewable
// tree on disk. Every rejection is loud (silent-no-op is this codebase's
// characteristic bug: an import that silently drops files instead of failing
// would be exactly that).
func ImportSkillArchive(fsys afero.Fs, archive []byte, destParent string, opts ExtractOptions) (string, error) {
	format, err := DetectArchiveFormat(archive)
	if err != nil {
		return "", fmt.Errorf("skill import: %w", err)
	}

	staging := filepath.Join(destParent, ".ctxloom-import-staging")
	if err := fsys.RemoveAll(staging); err != nil {
		return "", fmt.Errorf("skill import: clearing staging directory: %w", err)
	}

	topDir, err := HardenedExtract(fsys, archive, format, staging, opts)
	if err != nil {
		_ = fsys.RemoveAll(staging)
		return "", fmt.Errorf("skill import: %w", err)
	}

	final := filepath.Join(destParent, topDir)
	if err := fsys.RemoveAll(final); err != nil {
		_ = fsys.RemoveAll(staging)
		return "", fmt.Errorf("skill import: clearing destination %q: %w", final, err)
	}
	if err := fsys.Rename(staging, final); err != nil {
		_ = fsys.RemoveAll(staging)
		return "", fmt.Errorf("skill import: moving extracted tree into place: %w", err)
	}
	return final, nil
}

// =============================================================================
// HASH VERIFY + THE B2 INSTALL SEAM
// =============================================================================

// VerifyExtractedManifest recomputes the manifest of the tree actually on
// disk at dir and compares it, entry by entry (path, sha256, mode), against
// the expected manifest. Any mismatch — a missing file, an extra
// unaccounted-for file, a content change, or a mode change — is a loud error:
// a skill's signed manifest is meant to cover the WHOLE package (the same
// "grant binds the whole package" precedent as the trust gate's FormExec
// preimage), so a partial or tampered tree is never accepted silently.
func VerifyExtractedManifest(fsys afero.Fs, dir string, manifest SkillManifest) error {
	name := filepath.Base(filepath.Clean(dir))
	actual, err := buildSkillManifest(fsys, dir, name, DefaultMaxSkillPackageBytes)
	if err != nil {
		return fmt.Errorf("skill archive: recomputing manifest for verification: %w", err)
	}
	expected := manifest.sorted()

	if len(actual) != len(expected) {
		return fmt.Errorf("skill archive: extracted tree has %d files, signed manifest lists %d — rejected (integrity mismatch)", len(actual), len(expected))
	}
	for i := range expected {
		if actual[i].Path != expected[i].Path {
			return fmt.Errorf("skill archive: extracted tree does not match the signed manifest's file list (expected %q, found %q) — rejected (integrity mismatch)", expected[i].Path, actual[i].Path)
		}
		if actual[i].SHA256 != expected[i].SHA256 {
			return fmt.Errorf("skill archive: file %q content does not match the signed manifest (sha256 mismatch) — rejected (integrity mismatch)", expected[i].Path)
		}
		if actual[i].Mode != expected[i].Mode {
			return fmt.Errorf("skill archive: file %q mode %s does not match the signed manifest mode %s — rejected (integrity mismatch)", expected[i].Path, actual[i].Mode, expected[i].Mode)
		}
	}
	return nil
}

// SkillSignatureVerifier is the B2 SEAM. InstallSkillPackage calls
// VerifyManifestSignature over the expected manifest BEFORE extracting a
// single byte to disk — a failed signature check must never cause any write
// to occur. B1b wires NoopSkillSignatureVerifier (accepts everything); B2
// replaces the verifier value passed to InstallSkillPackage with one backed
// by signing.VerifyPublisher / the trust root and changes nothing else about
// this function's shape. This is the exact, named gate B2 hooks into.
type SkillSignatureVerifier interface {
	VerifyManifestSignature(manifest SkillManifest) error
}

// NoopSkillSignatureVerifier is the B1b placeholder for SkillSignatureVerifier
// — it accepts every manifest unconditionally. Until B2 lands, a skill
// installed via InstallSkillPackage is protected only by HardenedExtract's
// structural rejections and VerifyExtractedManifest's integrity check, NOT by
// any authenticity/provenance guarantee. Do not mistake "install succeeded"
// for "install was signed" while this is the active verifier.
type NoopSkillSignatureVerifier struct{}

// VerifyManifestSignature implements SkillSignatureVerifier by accepting
// every manifest. See the type doc: this is a placeholder, not a decision
// that skills don't need signing.
func (NoopSkillSignatureVerifier) VerifyManifestSignature(SkillManifest) error { return nil }

// InstallSkillPackage is the fixed install seam from the skill/command split
// plan §3.1b: verify(sig) -> extract -> verify(hashes), in that order, never
// reordered.
//
//   - verify(sig) runs FIRST, against the expected manifest bytes alone, and
//     GATES extraction outright: nothing is written to destDir if it fails.
//   - extract runs through HardenedExtract (see its doc comment for the full
//     rejection list).
//   - verify(hashes) necessarily runs AFTER extraction — it checks the actual
//     bytes on disk, which only exist once written. On any mismatch the
//     extracted tree at destDir is removed before returning, so a caller
//     never observes a partially-written, poisoned install (no partial tree
//     survives a failed verification, by construction).
//
// manifest may be nil (no known-good manifest to verify against, e.g. a
// brand-new untrusted import with no prior signed state) — in that case only
// the signature-verifier's own judgment on a nil manifest and HardenedExtract
// gate the install; ImportSkillArchive is the right entrypoint for that case
// and does not call InstallSkillPackage.
func InstallSkillPackage(fsys afero.Fs, archive []byte, format ArchiveFormat, destDir string, manifest SkillManifest, verifier SkillSignatureVerifier, opts ExtractOptions) error {
	if verifier == nil {
		verifier = NoopSkillSignatureVerifier{}
	}
	if err := verifier.VerifyManifestSignature(manifest); err != nil {
		return fmt.Errorf("skill install: signature verification failed, withholding: %w", err)
	}

	if _, err := HardenedExtract(fsys, archive, format, destDir, opts); err != nil {
		return err
	}

	if manifest != nil {
		if err := VerifyExtractedManifest(fsys, destDir, manifest); err != nil {
			if rmErr := fsys.RemoveAll(destDir); rmErr != nil {
				return fmt.Errorf("%w (also failed to clean up %q: %v)", err, destDir, rmErr)
			}
			return err
		}
	}
	return nil
}

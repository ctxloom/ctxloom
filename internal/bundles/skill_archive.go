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
	"strconv"
	"strings"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// This file holds Part B, slice B1b of the skill/command split: the ARCHIVE
// CODEC (pack a skill source tree into an Anthropic-Skills-API-shaped zip;
// unpack a zip or tar.gz back into a source tree) and the HARDENED EXTRACTOR
// that ImportSkillArchive routes every archive byte through.
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
	// The manifest is the authoritative file set, so an empty one means there
	// is nothing to pack — and a zip of nothing is a VALID zip: 22 bytes of
	// end-of-central-directory and a nil error, indistinguishable downstream
	// from a real export. A skill always has at least SKILL.md
	// (ParseSkillPackage requires it), so an empty manifest is a caller error,
	// never a legitimately empty package.
	if len(pkg.Manifest) == 0 {
		return nil, fmt.Errorf("skill archive: skill %q has an empty manifest — refusing to export an empty archive", pkg.Name)
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
//
// The whole string must be octal digits. SkillManifestEntry.Mode is bundle-tree
// data and therefore potentially remote-originated, so a partial parse is a
// rejection, not a value: strconv.ParseUint with an explicit base consumes the
// entire input or fails, unlike fmt.Sscanf's "%o", which stops at the first
// byte it cannot use and still reports success.
func parseSkillFileMode(mode string) (os.FileMode, error) {
	perm, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
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
// logic. Only kindFile and kindDir are ever written to disk; every other
// value — including the zero value — is a rejection.
type entryKind int

const (
	// kindUnknown is the ZERO VALUE on purpose: for an extractor whose whole
	// job is to reject, the default must be "refuse", not "write these bytes to
	// disk". An entry nothing has explicitly classified is rejected.
	kindUnknown entryKind = iota
	kindFile
	kindDir
	kindSymlink
	// kindOther covers hardlinks, device/char/block/fifo/socket special
	// files, and any tar typeflag this codec does not recognize. Tar can
	// carry all of these; zip cannot, so zip entries never classify as
	// kindOther.
	kindOther
)

// HardenedExtract is THE ONE function every archive byte passes through
// before landing on disk — used by ImportSkillArchive. It extracts archive
// (zip or tar.gz per format) into destDir, stripping the archive's single
// top-level directory (the skill's own directory, matched by name at
// ParseSkillPackage time) so the files land directly under destDir, and
// returns that top-level directory's original name.
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
// the rejected entry occurs, and the caller (ImportSkillArchive) is
// responsible for removing whatever was written for entries processed before
// the rejection — which it does.
func HardenedExtract(fsys afero.Fs, archive []byte, format ArchiveFormat, destDir string, opts ExtractOptions) (topDir string, err error) {
	opts = opts.normalized()

	// Every rejection this function can reach must be a no-op on the
	// filesystem, so the format verdict is settled BEFORE the extraction root
	// is created: an unsupported format leaves nothing behind at destDir.
	var extract func(afero.Fs, []byte, string, ExtractOptions) (string, error)
	switch format {
	case FormatZip:
		extract = extractZip
	case FormatTarGz:
		extract = extractTarGz
	default:
		return "", fmt.Errorf("skill archive: unsupported archive format")
	}

	if err := fsys.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("skill archive: creating extraction root %q: %w", destDir, err)
	}
	return extract(fsys, archive, destDir, opts)
}

func extractZip(fsys afero.Fs, archive []byte, destDir string, opts ExtractOptions) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return "", fmt.Errorf("skill archive: invalid zip: %w", err)
	}
	if len(zr.File) > opts.MaxEntries {
		return "", fmt.Errorf("skill archive: %d entries exceeds the %d entry cap — rejected (entry-count bomb guard)", len(zr.File), opts.MaxEntries)
	}

	var st extractState
	for _, f := range zr.File {
		kind := zipEntryKind(f)
		perr := func() error {
			rc, err := f.Open()
			if err != nil {
				return fmt.Errorf("opening entry %q: %w", f.Name, err)
			}
			defer rc.Close()
			return processArchiveEntry(fsys, destDir, &st, f.Name, kind, int64(f.Mode().Perm()), rc, opts)
		}()
		if perr != nil {
			return "", fmt.Errorf("skill archive: %w", perr)
		}
	}
	if st.topDir == "" {
		return "", fmt.Errorf("skill archive: empty archive, no top-level skill directory found")
	}
	// A topDir alone (e.g. a lone "myskill/" marker entry) is not
	// proof anything was actually extracted — processArchiveEntry returns nil
	// for the marker itself without writing a single file, so an archive
	// containing only single-segment entries previously "succeeded" having
	// written zero bytes under destDir.
	if st.filesWritten == 0 {
		return "", fmt.Errorf("skill archive: contained no files under its top-level directory %q — rejected", st.topDir)
	}
	return st.topDir, nil
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

	var st extractState
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
		if err := processArchiveEntry(fsys, destDir, &st, hdr.Name, kind, hdr.Mode, tr, opts); err != nil {
			return "", fmt.Errorf("skill archive: %w", err)
		}
	}
	if st.topDir == "" {
		return "", fmt.Errorf("skill archive: empty archive, no top-level skill directory found")
	}
	// See extractZip's identical guard — a top-level marker alone
	// is not proof anything was extracted.
	if st.filesWritten == 0 {
		return "", fmt.Errorf("skill archive: contained no files under its top-level directory %q — rejected", st.topDir)
	}
	return st.topDir, nil
}

func tarEntryKind(hdr *tar.Header) entryKind {
	switch hdr.Typeflag {
	case tar.TypeReg:
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

// extractState holds the per-archive accumulators HardenedExtract's zip and
// tar loops both thread through every entry: the archive's single top-level
// directory name (established by the first entry, enforced on every later one),
// the running total of uncompressed bytes actually read, and the number of
// files actually written. They live in one value so the choke point below takes
// a state rather than a fistful of pointers, and so the two format loops cannot
// drift in what they track.
type extractState struct {
	topDir       string
	total        int64
	filesWritten int
}

// processArchiveEntry is the single choke point HardenedExtract's zip and tar
// loops both funnel every entry through: path validation, kind rejection,
// entry-count/size accounting, mode normalization, and the actual write. See
// HardenedExtract's doc comment for the full, authoritative rejection list —
// this function and the four helpers below are where each rule is enforced, in
// this order: nothing is written until every check has passed.
func processArchiveEntry(fsys afero.Fs, destDir string, st *extractState, name string, kind entryKind, mode int64, r io.Reader, opts ExtractOptions) error {
	segments, err := validateEntryPath(name)
	if err != nil {
		return err
	}
	if err := rejectUnwritableKind(kind, name); err != nil {
		return err
	}
	rest, err := st.placeUnderTopDir(name, segments)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		// The top-level directory marker entry itself (e.g. "myskill/" in a
		// zip) — destDir already stands in for it, nothing further to write.
		return nil
	}

	target, err := confineEntryTarget(fsys, destDir, strings.Join(rest, "/"), name)
	if err != nil {
		return err
	}

	if kind == kindDir {
		if err := fsys.MkdirAll(target, 0o755); err != nil {
			return fmt.Errorf("creating directory for entry %q: %w", name, err)
		}
		return nil
	}
	return st.writeEntryFile(fsys, target, name, mode, r, opts)
}

// validateEntryPath rejects every path shape an archive entry may not have and
// returns the cleaned entry name split into segments. Absolute paths and any
// ".." segment are the canonical zip-slip shapes; a name that cleans away to
// nothing addresses the extraction root itself rather than anything inside it.
func validateEntryPath(name string) ([]string, error) {
	if name == "" {
		return nil, fmt.Errorf("entry has an empty name")
	}
	slashName := filepath.ToSlash(name)
	if path.IsAbs(slashName) || filepath.IsAbs(name) {
		return nil, fmt.Errorf("entry %q uses an absolute path — rejected (zip-slip guard)", name)
	}

	cleaned := path.Clean(slashName)
	if cleaned == "." || cleaned == "" {
		return nil, fmt.Errorf("entry %q resolves to an empty path", name)
	}
	segments := strings.Split(cleaned, "/")
	for _, seg := range segments {
		if seg == ".." || seg == "" {
			return nil, fmt.Errorf("entry %q contains a path-traversal segment — rejected (zip-slip guard)", name)
		}
	}
	return segments, nil
}

// rejectUnwritableKind admits only the two kinds that may ever land on disk.
// Everything else — a symlink, a hardlink/device/fifo/socket, or an entry no
// format reader classified at all — is a rejection, not a fallback.
func rejectUnwritableKind(kind entryKind, name string) error {
	switch kind {
	case kindUnknown:
		return fmt.Errorf("entry %q was not classified by any format reader — rejected (an unclassified entry is never written)", name)
	case kindSymlink:
		return fmt.Errorf("entry %q is a symlink — rejected (symlinks are never permitted in a skill archive)", name)
	case kindOther:
		return fmt.Errorf("entry %q is a hardlink, device, or other special file — rejected", name)
	}
	return nil
}

// placeUnderTopDir enforces the single-top-level-directory rule and returns the
// entry's segments BELOW that directory (empty for the marker entry itself).
// The first entry establishes the top-level name; a second, foreign top-level
// path is exactly the shape a zip-slip-adjacent smuggling attempt takes.
func (st *extractState) placeUnderTopDir(name string, segments []string) ([]string, error) {
	first := segments[0]
	switch {
	case st.topDir == "":
		st.topDir = first
	case first != st.topDir:
		return nil, fmt.Errorf("entry %q is outside the archive's single top-level directory %q", name, st.topDir)
	}
	return segments[1:], nil
}

// confineEntryTarget resolves relPath to a real filesystem path under destDir,
// through BOTH confinement checks — the lexical one and the on-disk one.
//
// safeSkillRelJoin (the same helper ResolveSkillDir uses for bundle-tree paths)
// is the canonical zip-slip guard: belt and braces, since validateEntryPath's
// segment scan already rejects "..", but every remote-originated relative path
// in this codebase is required to pass this same join-and-verify before it ever
// touches a real filesystem path.
//
// That check is purely LEXICAL (filepath.Join/Rel string arithmetic), so it
// cannot see that an ancestor directory of target might already be a real
// symlink on disk, planted in destDir before extraction began, pointing
// somewhere outside destDir entirely. No archive entry can introduce one
// mid-extraction (every symlink-typed entry is rejected), but the confinement
// guarantee must hold regardless of what destDir already contained — so any
// symlinks actually present along the path are resolved and confinement is
// re-verified against the resolved result. A check that cannot run fails
// closed.
func confineEntryTarget(fsys afero.Fs, destDir, relPath, name string) (string, error) {
	target, ok := safeSkillRelJoin(destDir, relPath)
	if !ok {
		return "", fmt.Errorf("entry %q escapes the extraction root — rejected (zip-slip guard)", name)
	}
	escapes, err := symlinkEscapesRoot(fsys, destDir, target)
	if err != nil {
		return "", fmt.Errorf("entry %q: checking for a pre-existing symlink escape: %w", name, err)
	}
	if escapes {
		return "", fmt.Errorf("entry %q escapes the extraction root via a pre-existing symlink — rejected (zip-slip guard)", name)
	}
	return target, nil
}

// writeEntryFile creates target's parent, streams the entry under the remaining
// byte budget, and writes it with a normalized mode.
//
// Decompression-bomb defense: never trust the archive's declared size. The
// stream passes through a hard limit sized to what is left of the budget, so an
// entry with more actual bytes than that comes back over budget and the whole
// extraction is rejected — the archive's own size fields never participate.
func (st *extractState) writeEntryFile(fsys afero.Fs, target, name string, mode int64, r io.Reader, opts ExtractOptions) error {
	if err := fsys.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("creating parent directory for entry %q: %w", name, err)
	}

	remaining := opts.MaxTotalBytes - st.total + 1
	if remaining < 0 {
		remaining = 0
	}
	data, err := io.ReadAll(io.LimitReader(r, remaining))
	if err != nil {
		return fmt.Errorf("reading entry %q: %w", name, err)
	}
	st.total += int64(len(data))
	if st.total > opts.MaxTotalBytes {
		return fmt.Errorf("uncompressed size exceeds the %d byte cap — rejected (decompression-bomb guard)", opts.MaxTotalBytes)
	}

	// AllowEmpty: a legitimately empty file (a placeholder, an emptied config)
	// is a normal archive entry — the decompression-bomb guard above already
	// caps what CAN be written, it says nothing about whether zero bytes is a
	// valid outcome, so refusing it here would reject a real, harmless entry.
	if err := iox.WriteFileAtomicFs(fsys, target, data, normalizeExtractedMode(mode), iox.AllowEmpty()); err != nil {
		return fmt.Errorf("writing entry %q: %w", name, err)
	}
	st.filesWritten++
	return nil
}

// symlinkEscapesRoot reports whether resolving any symlink ALREADY PRESENT
// under root, along the path to target, would land outside root. It exists
// because safeSkillRelJoin's confinement check is purely lexical (string
// path arithmetic) — it does not know that one of target's ancestor
// directories might be a symlink planted before extraction began, which the
// OS resolves at write time regardless of what the string looked like.
// HardenedExtract itself never introduces a symlink mid-extraction (every
// symlink-typed entry is rejected outright), so only a PRE-EXISTING symlink
// under destDir can trigger this — but the confinement guarantee must hold
// regardless of what destDir already contained when this function was
// called.
//
// Walks target's path one component at a time from root down, fully
// resolving (following chains of) any symlink it finds and re-checking
// confinement after each resolution. A component that does not exist yet is
// fine — HardenedExtract is about to create it — nothing further along the
// path can already be a symlink once we reach one. Filesystems that cannot
// represent a real symlink at all (afero.MemMapFs — its LstatIfPossible
// always reports ok=false, see afero's own TestMemFsLstatIfPossible) have
// nothing to check: there is no way a symlink escape could exist there.
func symlinkEscapesRoot(fsys afero.Fs, root, target string) (bool, error) {
	lstater, ok := fsys.(afero.Lstater)
	if !ok {
		return false, nil
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false, err
	}
	if rel == "." {
		return false, nil
	}

	cur := root
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." {
			continue
		}
		cur = filepath.Join(cur, seg)
		resolved, err := resolveSymlinkChain(fsys, lstater, cur)
		if err != nil {
			return false, err
		}
		if resolved == cur {
			continue
		}
		relResolved, err := filepath.Rel(root, resolved)
		if err != nil || relResolved == ".." || strings.HasPrefix(relResolved, ".."+string(filepath.Separator)) {
			return true, nil
		}
		cur = resolved
	}
	return false, nil
}

// maxSymlinkChain bounds resolveSymlinkChain's traversal so a maliciously
// looping or absurdly deep symlink chain planted in destDir cannot hang
// extraction — mirrors the kind of depth cap real OS symlink resolution
// (ELOOP) enforces.
const maxSymlinkChain = 40

// resolveSymlinkChain follows p through as many symlink hops as it actually
// is (zero if p is not a symlink, or does not exist yet), returning the
// final resolved path. Relative link targets are resolved against their
// symlink's own directory, matching real OS symlink semantics.
func resolveSymlinkChain(fsys afero.Fs, lstater afero.Lstater, p string) (string, error) {
	for range maxSymlinkChain {
		info, lstatOK, err := lstater.LstatIfPossible(p)
		if err != nil {
			if os.IsNotExist(err) {
				return p, nil // not created yet — nothing to resolve
			}
			return "", err
		}
		if !lstatOK || info.Mode()&os.ModeSymlink == 0 {
			return p, nil
		}
		reader, ok := fsys.(afero.LinkReader)
		if !ok {
			return "", fmt.Errorf("%q is a symlink but this filesystem cannot read it", p)
		}
		linkTarget, err := reader.ReadlinkIfPossible(p)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(linkTarget) {
			linkTarget = filepath.Join(filepath.Dir(p), linkTarget)
		}
		p = filepath.Clean(linkTarget)
	}
	return "", fmt.Errorf("symlink chain too deep at %q (possible loop)", p)
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
//
// validate, when non-nil, is run against the STAGING tree — before anything
// at the destination is touched — and its error aborts the import with the
// destination untouched. It is a required parameter rather than an option
// because the destination is computed from the ARCHIVE's own
// top-level directory name and then RemoveAll'd, so importing a malformed
// archive over a good skill used to leave the skill gone from disk while
// bundle.yaml still referenced it. Callers with genuinely nothing to check
// pass nil, and do so visibly.
func ImportSkillArchive(fsys afero.Fs, archive []byte, destParent string, opts ExtractOptions, validate func(afero.Fs, string) error) (string, error) {
	format, err := DetectArchiveFormat(archive)
	if err != nil {
		return "", fmt.Errorf("skill import: %w", err)
	}

	// stagingRoot holds ONE directory whose name is the archive's top-level
	// name, because a skill package is identified by its directory's base
	// name (ParseSkillPackage derives the skill name from it and matches it
	// against the frontmatter). Validating a tree parked under a
	// staging-shaped name would fail for the wrong reason and never catch
	// the real one, so staging reproduces the final NAME as well as the
	// final contents.
	stagingRoot := filepath.Join(destParent, ".ctxloom-import-staging")
	if err := fsys.RemoveAll(stagingRoot); err != nil {
		return "", fmt.Errorf("skill import: clearing staging directory: %w", err)
	}
	extracted := filepath.Join(stagingRoot, "tree")

	topDir, err := HardenedExtract(fsys, archive, format, extracted, opts)
	if err != nil {
		_ = fsys.RemoveAll(stagingRoot)
		return "", fmt.Errorf("skill import: %w", err)
	}

	// HardenedExtract STRIPS the top-level directory, so `extracted` holds
	// what will become destParent/topDir; give it that name in staging.
	staged := filepath.Join(stagingRoot, topDir)
	if staged != extracted {
		if err := fsys.Rename(extracted, staged); err != nil {
			_ = fsys.RemoveAll(stagingRoot)
			return "", fmt.Errorf("skill import: naming the staged tree %q: %w", topDir, err)
		}
	}

	// Validate in staging: the destination must not be destroyed on behalf of
	// a replacement that turns out to be unusable.
	if validate != nil {
		if err := validate(fsys, staged); err != nil {
			_ = fsys.RemoveAll(stagingRoot)
			return "", fmt.Errorf("skill import: %w", err)
		}
	}

	// Swap, never clear-then-hope. RemoveAll(final) followed by Rename leaves a
	// window in which the previously-good tree is already gone and the
	// replacement has not arrived: a rename that fails there (cross-device,
	// EACCES, a concurrent hold on the directory) leaves the user with NEITHER
	// tree. So any existing destination is moved ASIDE, and put back if the
	// swap does not complete. The aside copy lives inside stagingRoot so the
	// ordinary staging cleanup reclaims it.
	final := filepath.Join(destParent, topDir)
	aside := filepath.Join(stagingRoot, ".replaced")
	replaced, err := afero.Exists(fsys, final)
	if err != nil {
		_ = fsys.RemoveAll(stagingRoot)
		return "", fmt.Errorf("skill import: inspecting destination %q: %w", final, err)
	}
	if replaced {
		if err := fsys.Rename(final, aside); err != nil {
			_ = fsys.RemoveAll(stagingRoot)
			return "", fmt.Errorf("skill import: moving the existing tree at %q aside: %w", final, err)
		}
	}
	if err := fsys.Rename(staged, final); err != nil {
		if replaced {
			if rerr := fsys.Rename(aside, final); rerr != nil {
				// Both the swap and the restore failed: say so, and say where
				// the only surviving copy is. Reporting just the swap failure
				// would send the user looking at an empty destination with no
				// idea their tree is still recoverable.
				return "", fmt.Errorf("skill import: moving extracted tree into place failed (%w) and the previous tree could NOT be restored to %q (%v) — it is still at %q", err, final, rerr, aside)
			}
		}
		_ = fsys.RemoveAll(stagingRoot)
		return "", fmt.Errorf("skill import: moving extracted tree into place: %w", err)
	}
	_ = fsys.RemoveAll(stagingRoot)
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
// "grant binds the whole package" precedent as the executable preimage a
// countersignature over an "exec/mcp" or "exec/hook" item covers), so a partial
// or tampered tree is never accepted silently.
//
// Three of those four are integrity: the file list and the hashes are what a
// signature attests, so a difference there is tampering or corruption and is
// named as such. A MODE difference is not. A mode bit is not portable, the
// digest deliberately excludes it, and the manifest's mode comes from the
// package's own `executable:` DECLARATION — so the two sides of a mode
// comparison are a declaration and a filesystem, and disagreement means the
// author has not said what they meant. Calling that tampering sent authors
// hunting for an attacker over a missing YAML line; skillModeDisagreement says
// what is actually wrong.
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
			return skillModeDisagreement(expected[i].Path, expected[i].Mode, actual[i].Mode)
		}
	}
	return nil
}

// skillModeDisagreement reports a file whose DECLARED mode and on-disk mode
// disagree, in the terms the author can act on.
//
// It never says "integrity mismatch". The declaration lives inside the hashed,
// signed bytes; the filesystem's bit does not, and no signature covers it — so
// a mode difference is never evidence of tampering, and the one case that
// produces it in practice is an author who chmod +x'd a script and did not add
// it to `executable:`. The package is still refused (the declaration and the
// tree have to agree before anything is delivered), but refused for the reason
// it is actually refused for, naming the line to add.
func skillModeDisagreement(path, declared, onDisk string) error {
	const (
		exec  = "0755"
		plain = "0644"
	)
	switch {
	case declared == plain && onDisk == exec:
		return fmt.Errorf("skill package: file %q is DECLARED non-executable (mode %s) but is executable (mode %s) on disk — "+
			"the declaration is what travels (a mode bit is not portable and no signature covers it), so this file would be "+
			"delivered non-executable and would not run; add %q to the package sidecar's executable: list and re-sign, "+
			"or clear its exec bit. This is a declaration that disagrees with the tree, NOT tampering: the file's contents verify",
			path, declared, onDisk, path)
	case declared == exec && onDisk == plain:
		return fmt.Errorf("skill package: file %q is DECLARED executable (mode %s) but is non-executable (mode %s) on disk — "+
			"the exec bit was lost somewhere between authoring and here (a checkout on a filesystem without one, an archive "+
			"that dropped it, a umask); restore it, or remove %q from the package sidecar's executable: list. "+
			"This is a declaration that disagrees with the tree, NOT tampering: the file's contents verify",
			path, declared, onDisk, path)
	default:
		return fmt.Errorf("skill package: file %q is DECLARED mode %s but is mode %s on disk — "+
			"the declaration and the tree must agree before the package is delivered; fix whichever is wrong and re-sign. "+
			"This is a declaration that disagrees with the tree, NOT tampering: the file's contents verify",
			path, declared, onDisk)
	}
}

// PublisherSkillSignatureVerifier is the PRODUCTION skill-manifest signature
// verifier (Part B2): it verifies a detached armored signature over the expected
// manifest's canonical bytes (SkillManifest.Serialize()) using the EXACT same
// signature envelope and trust-root machinery every other ctxloom signature
// check uses (signing.VerifyPublisher against the publish namespace, resolved
// through the allowed_signers TrustRoot) — no new crypto, no parallel scheme.
//
// Unlike VerifyPublisher's ordinary three-outcome contract (unsigned-to-you
// is not an error — spec §10.1), an INSTALL is a production-safety gate: a
// skill with no signature, or one by a key this machine does not trust to
// publish, must not install silently. Both cases here return an error —
// "withhold and tell the human", never "install anyway, unsigned". A skill
// installed via the ordinary git-clone bundle tree (not this archive path)
// still goes through the review/accept flow instead; this verifier only
// gates the archive-sourced install path (skill/command split plan §3.1b).
type PublisherSkillSignatureVerifier struct {
	// ArmoredSignature is the detached publish-namespace signature over
	// manifest.Serialize() (e.g. a `.sig` sidecar shipped alongside a stored
	// skill archive/manifest, or an imported archive's ctxloom-namespaced
	// signature entry). Empty means "no signature at all" — always rejected.
	ArmoredSignature []byte
	// Root resolves which keys are trusted to publish. A nil Root trusts no
	// key — fails closed, like every other TrustRoot consumer in this
	// codebase.
	Root signing.TrustRoot
	// Now is a seam for tests to pin time; nil means time.Now.
	Now func() time.Time
}

// VerifyManifestSignature verifies v.ArmoredSignature covers
// manifest.Serialize() exactly, signed by a key v.Root trusts for the publish
// namespace. manifest is always the expected (previously-signed) manifest the
// caller already has in hand — it is never recomputed from the archive here;
// that is VerifyExtractedManifest's job, which runs AFTER extraction against
// the same manifest value.
func (v PublisherSkillSignatureVerifier) VerifyManifestSignature(manifest SkillManifest) error {
	if len(v.ArmoredSignature) == 0 {
		return fmt.Errorf("no signature present for this skill package — an unsigned skill must go through ctxloom review, not install")
	}
	now := time.Now
	if v.Now != nil {
		now = v.Now
	}
	principal, err := signing.VerifyPublisher(manifest.Serialize(), v.ArmoredSignature, v.Root, now())
	if err != nil {
		// ErrSignatureTampered: a present signature that does not honestly
		// cover these bytes. Never benign — propagate as-is.
		return err
	}
	if principal == "" {
		// VerifyPublisher's "unsigned to you" outcome: the signature is
		// well-formed but by a key this machine does not trust to publish.
		// Ordinarily that takes the review path; for an install it is a hard
		// stop — an untrusted publisher's skill must not land on disk.
		return fmt.Errorf("signature is not by a publisher this machine trusts for %s — withholding", signing.NamespacePublish)
	}
	return nil
}

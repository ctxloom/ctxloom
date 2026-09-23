package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"

	"github.com/ctxloom/ctxloom/internal/core/release"
)

// ManifestVersionMarker is the first line of a bundle manifest. It is NOT
// DigestVersionMarker, and the difference is load-bearing: a per-form content
// digest has the same entry-line shape, so if the two shared a marker a
// signature over a form digest could be replayed as a bundle manifest. A
// manifest parser refuses the digest marker; a digest is never parsed as one.
const ManifestVersionMarker = "# ctxloom-bundle-manifest/1"

// The release header's line prefixes, in the one order they may appear.
const (
	headerName      = "# name: "
	headerVersion   = "# version: "
	headerRetracts  = "# retracts: "
	headerWithdrawn = "# withdrawn: "
)

// ManifestPath is the bundle-relative path of the tree manifest: the ONE
// bundle-level object that says "these paths, with these hashes, are what I
// published".
//
// It is named SHA256SUMS and its entry lines are the coreutils shape Digest
// also produces, so a consumer with no ctxloom at all can check a pulled tree
// with stock `sha256sum -c SHA256SUMS`. That interoperability is the reason the
// format was adopted rather than invented. Everything else the manifest says —
// the release header — rides on `#` lines, which that tool skips.
//
// The manifest is a FIRST-CLASS BUNDLE-LEVEL OBJECT, deliberately NOT an item.
// Manifest-as-reserved-item was considered and rejected: it would make the
// manifest enumerate items while itself being one — leaving "does it list
// itself" genuinely ambiguous — and it would carry an ITEM signature where a
// BUNDLE-LEVEL signature is what a consumer needs.
const ManifestPath = "SHA256SUMS"

// BundleSigKey is the fixed signature-store key the bundle signature is filed
// under.
//
// It is FIXED, not derived from the manifest's content, and that buys one
// property: if an attacker rewrites the manifest to cover the file they added,
// a content-keyed signature would move out from under its own key, become
// unreachable, and the bundle would present as UNSIGNED — attestation stripped
// by editing. Filed at a fixed key the old signature stays reachable, fails to
// verify over the new manifest bytes, and the bundle presents as TAMPERED.
const BundleSigKey = ManifestPath

// The manifest error vocabulary. Callers match with errors.Is.
var (
	// ErrManifestMissing reports a bundle with no manifest at all. It is
	// distinct from a malformed one on purpose: "never signed" and "signed and
	// then mangled" are different situations and only one of them is ordinary.
	ErrManifestMissing = errors.New("content: bundle has no manifest")
	// ErrManifestFormat reports a manifest whose bytes are not the canonical
	// rendering. There is no lenient parse: a manifest read one way and
	// re-rendered another would be signed as one byte string and checked as a
	// different one.
	ErrManifestFormat = errors.New("content: malformed manifest")
	// ErrContentsMismatch reports a tree that does not match its manifest, in
	// either direction. Inspect the wrapped *ContentsError for which.
	ErrContentsMismatch = errors.New("content: tree does not match its manifest")
	// ErrUnclaimed reports files inside a kind directory that no registered
	// SurfaceType recognises. Enumeration FAILS on these rather than skipping
	// them: a silently dropped file is how a mis-extensioned hook vanishes and
	// how an added file rides along uncovered.
	ErrUnclaimed = errors.New("content: unclaimed file in a kind directory")
)

// ManifestEntry is one covered path and the hex sha256 of its bytes.
type ManifestEntry struct {
	Path   string
	SHA256 string
}

// Manifest is a bundle's path -> hash map: the tree's SHAPE. It is one of the
// two layers signing rests on; the other is the signature store, which is a
// hash -> signatures map. Keeping them separate is what lets the manifest be
// the link (path -> hash -> signature) with no second, drift-prone pointer.
//
// It also carries the RELEASE the publisher is signing: the bundle's name, its
// version, and the earlier versions it withdraws. Those are signed for the same
// reason the hashes are — a name the signature does not cover lets a trusted
// tree be served as another bundle, a version it does not cover lets an old
// signed tree be served as current, and retractions it does not cover can be
// stripped by whoever controls the repository.
//
// The zero Manifest is empty and carries no claims; use IsZero to tell it from
// a loaded one.
type Manifest struct {
	rel     release.Release
	entries []ManifestEntry
	index   map[string]string
}

// NewManifest builds a manifest for rel from entries, sorting and validating
// both. Retractions are sorted by version.
func NewManifest(rel release.Release, entries []ManifestEntry) (Manifest, error) {
	rel, err := canonicalRelease(rel)
	if err != nil {
		return Manifest{}, err
	}
	if len(entries) == 0 {
		return Manifest{}, fmt.Errorf("%w: a manifest must cover at least one file", ErrManifestFormat)
	}
	cloned := make([]ManifestEntry, len(entries))
	copy(cloned, entries)
	sort.Slice(cloned, func(i, j int) bool { return cloned[i].Path < cloned[j].Path })
	index := make(map[string]string, len(cloned))
	for _, e := range cloned {
		if err := validateDigestPath(e.Path); err != nil {
			return Manifest{}, fmt.Errorf("%w: %w", ErrManifestFormat, err)
		}
		if !hexSHA256.MatchString(e.SHA256) {
			return Manifest{}, fmt.Errorf("%w: %q has hash %q, want 64 lowercase hex characters", ErrManifestFormat, e.Path, e.SHA256)
		}
		if _, dup := index[e.Path]; dup {
			return Manifest{}, fmt.Errorf("%w: duplicate path %q", ErrManifestFormat, e.Path)
		}
		index[e.Path] = e.SHA256
	}
	return Manifest{rel: rel, entries: cloned, index: index}, nil
}

// canonicalRelease validates a release for rendering and returns a copy with
// its retractions sorted. Every field lands on one header line, so each must be
// a single line with nothing a strict re-render would normalise away.
func canonicalRelease(rel release.Release) (release.Release, error) {
	if err := validateBundleID(BundleID(rel.Name)); err != nil {
		return release.Release{}, fmt.Errorf("%w: release name: %w", ErrManifestFormat, err)
	}
	if err := headerField("name", rel.Name); err != nil {
		return release.Release{}, err
	}
	if rel.Version == nil {
		return release.Release{}, fmt.Errorf("%w: release %q has no version", ErrManifestFormat, rel.Name)
	}
	out := release.Release{Name: rel.Name, Version: rel.Version, Withdrawn: rel.Withdrawn}
	out.Retracts = make([]release.Retraction, len(rel.Retracts))
	copy(out.Retracts, rel.Retracts)
	for _, r := range out.Retracts {
		if r.Version == nil {
			return release.Release{}, fmt.Errorf("%w: a retraction names no version", ErrManifestFormat)
		}
		if err := headerField("retraction reason", r.Reason); err != nil {
			return release.Release{}, err
		}
	}
	sort.SliceStable(out.Retracts, func(i, j int) bool { return out.Retracts[i].Version.LessThan(out.Retracts[j].Version) })
	for i := 1; i < len(out.Retracts); i++ {
		if out.Retracts[i].Version.Equal(out.Retracts[i-1].Version) {
			return release.Release{}, fmt.Errorf("%w: version %s is retracted twice", ErrManifestFormat, out.Retracts[i].Version)
		}
	}
	if rel.Withdrawn != "" {
		if err := headerField("withdrawal reason", rel.Withdrawn); err != nil {
			return release.Release{}, err
		}
	}
	return out, nil
}

// headerField refuses a value that cannot sit on one header line and survive a
// strict re-render: empty, multi-line, containing any control character, or
// padded with whitespace.
func headerField(what, v string) error {
	if v == "" {
		return fmt.Errorf("%w: empty %s", ErrManifestFormat, what)
	}
	if strings.TrimSpace(v) != v {
		return fmt.Errorf("%w: %s %q has surrounding whitespace", ErrManifestFormat, what, v)
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %s %q is not a single line", ErrManifestFormat, what, v)
		}
	}
	return nil
}

// parseStrictVersion reads a header version. Strict semver only: a loose form
// ("v1.2", "1.2") would render back differently and break byte equality, and a
// version floor compared across two spellings of one version is not a floor.
func parseStrictVersion(s string) (*semver.Version, error) {
	v, err := semver.StrictNewVersion(s)
	if err != nil {
		return nil, fmt.Errorf("%w: version %q is not strict semver: %w", ErrManifestFormat, s, err)
	}
	return v, nil
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ParseManifest decodes manifest bytes.
//
// It is STRICT and re-renders what it parsed, requiring byte equality with the
// input. A tolerant parser would let a signature cover one byte string while
// verification reasoned about another — the exact shape of bug that makes a
// signed artifact mean something different to the signer than to the verifier.
// So a trailing space, a CRLF, an out-of-order line or a single-space separator
// is refused rather than normalised.
func ParseManifest(raw []byte) (Manifest, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	body := make([]string, len(lines))
	for i, line := range lines {
		b, ok := strings.CutSuffix(line, "\n")
		if !ok {
			return Manifest{}, fmt.Errorf("%w: last line %q is not newline-terminated", ErrManifestFormat, line)
		}
		body[i] = b
	}
	if len(body) == 0 || body[0] != ManifestVersionMarker {
		first := ""
		if len(body) > 0 {
			first = body[0]
		}
		return Manifest{}, fmt.Errorf("%w: version marker is %q, this build understands only %q", ErrManifestFormat, first, ManifestVersionMarker)
	}
	rel, rest, err := parseReleaseHeader(body[1:])
	if err != nil {
		return Manifest{}, err
	}
	entries := make([]ManifestEntry, 0, len(rest))
	for _, line := range rest {
		hash, path, ok := strings.Cut(line, "  ")
		if !ok {
			return Manifest{}, fmt.Errorf("%w: line %q is not %q", ErrManifestFormat, line, "<hash><two spaces><path>")
		}
		entries = append(entries, ManifestEntry{Path: path, SHA256: hash})
	}
	m, err := NewManifest(rel, entries)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(m.Bytes(), raw) {
		return Manifest{}, fmt.Errorf("%w: not in canonical form (header in order name, version, retracts sorted by version, withdrawn; entries sorted by path, separated by two spaces, LF-terminated)", ErrManifestFormat)
	}
	return m, nil
}

// parseReleaseHeader reads the header lines in their one legal order and
// returns the release and the lines after it. Any other `#` line — an unknown
// key, or a known one out of place — is refused rather than skipped: a header
// field this build ignores is a claim the signature covers and nobody checks.
func parseReleaseHeader(lines []string) (release.Release, []string, error) {
	var rel release.Release
	i := 0
	next := func(prefix string) (string, bool) {
		if i < len(lines) {
			if v, ok := strings.CutPrefix(lines[i], prefix); ok {
				i++
				return v, true
			}
		}
		return "", false
	}
	name, ok := next(headerName)
	if !ok {
		return release.Release{}, nil, fmt.Errorf("%w: missing %q line", ErrManifestFormat, strings.TrimSpace(headerName))
	}
	rel.Name = name
	ver, ok := next(headerVersion)
	if !ok {
		return release.Release{}, nil, fmt.Errorf("%w: missing %q line after the name", ErrManifestFormat, strings.TrimSpace(headerVersion))
	}
	v, err := parseStrictVersion(ver)
	if err != nil {
		return release.Release{}, nil, err
	}
	rel.Version = v
	for {
		r, ok := next(headerRetracts)
		if !ok {
			break
		}
		vs, reason, _ := strings.Cut(r, " ")
		rv, err := parseStrictVersion(vs)
		if err != nil {
			return release.Release{}, nil, err
		}
		rel.Retracts = append(rel.Retracts, release.Retraction{Version: rv, Reason: reason})
	}
	if w, ok := next(headerWithdrawn); ok {
		rel.Withdrawn = w
	}
	rest := lines[i:]
	for _, line := range rest {
		if strings.HasPrefix(line, "#") {
			return release.Release{}, nil, fmt.Errorf("%w: header line %q is unknown or out of order", ErrManifestFormat, line)
		}
	}
	return rel, rest, nil
}

// Bytes renders the manifest in canonical form — the bytes a bundle signature
// covers.
func (m Manifest) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString(ManifestVersionMarker)
	buf.WriteByte('\n')
	if m.rel.Version != nil {
		buf.WriteString(headerName + m.rel.Name + "\n")
		buf.WriteString(headerVersion + m.rel.Version.String() + "\n")
		for _, r := range m.rel.Retracts {
			buf.WriteString(headerRetracts + r.Version.String() + " " + r.Reason + "\n")
		}
		if m.rel.Withdrawn != "" {
			buf.WriteString(headerWithdrawn + m.rel.Withdrawn + "\n")
		}
	}
	for _, e := range m.entries {
		buf.WriteString(e.SHA256)
		buf.WriteString("  ")
		buf.WriteString(e.Path)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// Release is the release the manifest's signature covers.
func (m Manifest) Release() release.Release {
	out := m.rel
	out.Retracts = append([]release.Retraction(nil), m.rel.Retracts...)
	return out
}

// Entries returns the covered entries, sorted by path.
func (m Manifest) Entries() []ManifestEntry {
	out := make([]ManifestEntry, len(m.entries))
	copy(out, m.entries)
	return out
}

// Lookup returns the hash recorded for a bundle-relative path.
func (m Manifest) Lookup(p string) (string, bool) {
	h, ok := m.index[p]
	return h, ok
}

// Len is the number of covered paths.
func (m Manifest) Len() int { return len(m.entries) }

// IsZero reports a manifest carrying no claims.
func (m Manifest) IsZero() bool { return len(m.entries) == 0 }

// ManifestCovers reports whether a bundle-relative path is subject to manifest
// coverage. It is the ONE place the two exemptions are stated, so no caller can
// invent a third.
//
// Exactly two paths are exempt, and both exemptions are STRUCTURAL rather than
// policy:
//
//   - The manifest itself. A file whose content records its own hash has no
//     fixed point.
//   - Everything under .sigs/. Signatures are written AFTER the manifest is
//     built and signed; covering them would mean signing the bundle changes the
//     bytes the bundle signature covers.
//
// The obvious objection is that adding or removing a signature is then
// undetectable. It is — and it is SAFE: the only signatures are over the
// manifest, so an added one from an untrusted key is inert, an added one from
// a trusted key over these exact bytes attests what the manifest already says,
// and removing one can at most leave the bundle unattested, never attested by
// someone else.
func ManifestCovers(p string) bool {
	return p != ManifestPath && !strings.HasPrefix(p, SigDirName+"/")
}

// BuildManifest hashes every covered file in a bundle.
//
// It reads through the store — Bundle.Files and Bundle.ReadFile — and never
// touches a filesystem of its own, which is what lets it serve a pinned-remote
// or archive-backed bundle unchanged. It also covers files no SurfaceType
// claims at the BUNDLE root (a README, a LICENSE): coverage is total by path,
// not by recognisability, because a manifest that only covered recognised items
// would leave exactly the unrecognised ones as the laundering channel.
//
// It refuses a release named for any bundle but b: the name is what stops a
// signed tree from verifying under another bundle's path, so signing one tree
// under another's name is never what a publisher meant.
func BuildManifest(ctx context.Context, b Bundle, rel release.Release) (Manifest, error) {
	if rel.Name != string(b.ID()) {
		return Manifest{}, fmt.Errorf("content: refusing to build a manifest for bundle %q under the release name %q", b.ID(), rel.Name)
	}
	files, err := b.Files(ctx)
	if err != nil {
		return Manifest{}, err
	}
	entries := make([]ManifestEntry, 0, len(files))
	for _, f := range files {
		if !ManifestCovers(f) {
			continue
		}
		data, err := b.ReadFile(ctx, f)
		if err != nil {
			return Manifest{}, err
		}
		sum := sha256.Sum256(data)
		entries = append(entries, ManifestEntry{Path: f, SHA256: hex.EncodeToString(sum[:])})
	}
	if len(entries) == 0 {
		// An empty manifest would hash to a constant shared by every empty
		// bundle, so one bundle's signature would verify another's. Refuse.
		return Manifest{}, fmt.Errorf("content: bundle %q has no files a manifest could cover", b.ID())
	}
	return NewManifest(rel, entries)
}

// ContentsError is the two-directional result of VerifyContents. Each list is
// reported separately because they mean different things: Missing and
// Mismatched say the publisher's own claims do not hold, while Unclaimed says
// something is present that the publisher never claimed at all.
type ContentsError struct {
	Bundle BundleID
	// Missing are manifest entries with no file on disk.
	Missing []string
	// Mismatched are files whose bytes hash differently than the manifest says.
	Mismatched []string
	// Unclaimed are covered-eligible files on disk that the manifest never
	// mentions — the added-file channel.
	Unclaimed []string
}

func (e *ContentsError) Error() string {
	var parts []string
	if len(e.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing (%s)", len(e.Missing), strings.Join(e.Missing, ", ")))
	}
	if len(e.Mismatched) > 0 {
		parts = append(parts, fmt.Sprintf("%d altered (%s)", len(e.Mismatched), strings.Join(e.Mismatched, ", ")))
	}
	if len(e.Unclaimed) > 0 {
		parts = append(parts, fmt.Sprintf("%d not covered by the manifest (%s)", len(e.Unclaimed), strings.Join(e.Unclaimed, ", ")))
	}
	return fmt.Sprintf("content: bundle %q does not match its manifest: %s", e.Bundle, strings.Join(parts, "; "))
}

func (e *ContentsError) Unwrap() error { return ErrContentsMismatch }

// VerifyContents checks the tree against the manifest in BOTH directions.
//
// Forwards — every manifest entry names a file that exists and hashes to the
// recorded value — is the direction that catches editing and deletion.
// Backwards — every covered-eligible file on disk appears in the manifest — is
// the direction that catches ADDITION, and it is the one that makes "this tree
// is what the publisher published" an enforceable claim rather than an
// aspiration. Without it a hostile publisher's extra directory is invisible:
// nothing enumerates it as an item, so nothing would ever look at it.
//
// It reports EVERY problem it finds rather than the first, because a partial
// diagnosis of a tampered tree invites fixing one file and re-running.
func (m Manifest) VerifyContents(ctx context.Context, b Bundle) error {
	if m.IsZero() {
		return fmt.Errorf("%w: refusing to verify %q against an empty manifest", ErrManifestMissing, b.ID())
	}
	files, err := b.Files(ctx)
	if err != nil {
		return err
	}
	onDisk := make(map[string]struct{}, len(files))
	out := &ContentsError{Bundle: b.ID()}
	for _, f := range files {
		if !ManifestCovers(f) {
			continue
		}
		onDisk[f] = struct{}{}
		if _, claimed := m.Lookup(f); !claimed {
			out.Unclaimed = append(out.Unclaimed, f)
		}
	}
	for _, e := range m.entries {
		if _, ok := onDisk[e.Path]; !ok {
			out.Missing = append(out.Missing, e.Path)
			continue
		}
		data, err := b.ReadFile(ctx, e.Path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != e.SHA256 {
			out.Mismatched = append(out.Mismatched, e.Path)
		}
	}
	sort.Strings(out.Missing)
	sort.Strings(out.Mismatched)
	sort.Strings(out.Unclaimed)
	if len(out.Missing) == 0 && len(out.Mismatched) == 0 && len(out.Unclaimed) == 0 {
		return nil
	}
	return out
}

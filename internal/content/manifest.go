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
)

// ManifestPath is the bundle-relative path of the tree manifest: the ONE
// bundle-level object that says "these paths, with these hashes, are what I
// published".
//
// It is named SHA256SUMS and rendered in exactly the format Digest produces, so
// a consumer with no ctxloom at all can check a pulled tree with stock
// `sha256sum -c SHA256SUMS`. That interoperability is the reason the format was
// adopted rather than invented, and it is why the manifest and a form's content
// digest share one renderer instead of two that could drift.
//
// The manifest is a FIRST-CLASS BUNDLE-LEVEL OBJECT, deliberately NOT an item.
// Manifest-as-reserved-item was considered and rejected: it would make the
// manifest enumerate items while itself being one — leaving "does it list
// itself" genuinely ambiguous — and it would carry an ITEM signature where a
// BUNDLE-LEVEL signature is what a consumer needs.
const ManifestPath = "SHA256SUMS"

// BundleSigKey is the fixed signature-store key the BUNDLE-level signature is
// filed under, in place of the content-derived key every per-form signature
// uses.
//
// This is a deliberate divergence from content-keying, and it buys the one
// property content-keying cannot give at bundle level: if an attacker rewrites
// the manifest to cover the file they added, a content-keyed signature would
// move out from under its own key, become unreachable, and the bundle would
// present as UNSIGNED — attestation stripped by editing. Filed at a fixed key
// the old signature stays reachable, fails to verify over the new manifest
// bytes, and the bundle presents as TAMPERED. The rename-cannot-orphan argument
// that motivates content-keying does not apply here: a bundle has exactly one
// manifest, at exactly one path, forever.
//
// It cannot collide with a content key: content keys are 64 lowercase hex
// characters and this is not.
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
// The zero Manifest is empty and carries no claims; use IsZero to tell it from
// a loaded one.
type Manifest struct {
	entries []ManifestEntry
	index   map[string]string
}

// NewManifest builds a manifest from entries, sorting and validating them.
func NewManifest(entries []ManifestEntry) (Manifest, error) {
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
	return Manifest{entries: cloned, index: index}, nil
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
	marker, rest, ok := bytes.Cut(raw, []byte("\n"))
	if !ok {
		return Manifest{}, fmt.Errorf("%w: no version marker line", ErrManifestFormat)
	}
	if string(marker) != DigestVersionMarker {
		return Manifest{}, fmt.Errorf("%w: version marker is %q, this build understands only %q", ErrManifestFormat, marker, DigestVersionMarker)
	}
	var entries []ManifestEntry
	for _, line := range strings.SplitAfter(string(rest), "\n") {
		if line == "" {
			continue
		}
		body, ok := strings.CutSuffix(line, "\n")
		if !ok {
			return Manifest{}, fmt.Errorf("%w: last line %q is not newline-terminated", ErrManifestFormat, line)
		}
		hash, path, ok := strings.Cut(body, "  ")
		if !ok {
			return Manifest{}, fmt.Errorf("%w: line %q is not %q", ErrManifestFormat, body, "<hash><two spaces><path>")
		}
		entries = append(entries, ManifestEntry{Path: path, SHA256: hash})
	}
	m, err := NewManifest(entries)
	if err != nil {
		return Manifest{}, err
	}
	if !bytes.Equal(m.Bytes(), raw) {
		return Manifest{}, fmt.Errorf("%w: not in canonical form (entries must be sorted by path, separated by two spaces, LF-terminated)", ErrManifestFormat)
	}
	return m, nil
}

// Bytes renders the manifest in canonical form — the bytes a bundle signature
// covers.
func (m Manifest) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteString(DigestVersionMarker)
	buf.WriteByte('\n')
	for _, e := range m.entries {
		buf.WriteString(e.SHA256)
		buf.WriteString("  ")
		buf.WriteString(e.Path)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
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
// undetectable. It is — and it is SAFE, for a reason worth stating rather than
// assuming. Removing a per-item signature cannot downgrade anything: the item's
// bytes must still match the signed manifest, so the item stays attested (by
// the manifest's signer, who is named in the verdict) or, if the bytes do not
// match, drops from "content-substituted" to "tampered" — strictly more
// suspicious, never less. Adding one cannot upgrade anything either: a
// signature from an untrusted key is inert, one from a trusted key over
// manifest-matching bytes changes only which trusted principal is named, and
// one from a trusted key over bytes the manifest contradicts produces the
// content-substituted verdict rather than silent acceptance. What a
// tree-writer cannot do is CHOOSE which attestation governs in their favour.
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
func BuildManifest(ctx context.Context, b Bundle) (Manifest, error) {
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
	return NewManifest(entries)
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

package content

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// DigestVersionMarker is the first line of every Content digest. It is a
// comment line, which `sha256sum -c` skips (verified against GNU coreutils
// 9.7), so a digest remains checkable with the stock tool while still carrying
// an explicit format version. A version is an IDENTITY, not a constraint: a
// later format bumps this string, and a verifier that does not recognise it
// must fail loudly rather than guess.
const DigestVersionMarker = "# ctxloom-content-digest/1"

// Digest renders the deterministic content digest for a set of components.
//
// The format is the coreutils SHA256SUMS shape — "<64 lowercase hex><two
// spaces><bundle-relative path>", one line per component, LF-terminated —
// preceded by DigestVersionMarker. Lines are sorted byte-wise on path. Because
// the paths are exactly Component.Path, a digest can be verified against a
// bundle root with `sha256sum -c`; that interoperability is why the format was
// adopted rather than invented.
//
// What is deliberately ABSENT:
//
//   - MODE BITS. Mode is not portable — on Windows Go toggles only the
//     read-only bit — so a mode-bearing digest would be platform-dependent and
//     a checkout there would fail its own signature. Executability is declared
//     metadata inside the hashed bytes instead (see ComponentMode).
//   - mtime, uid, gid, and anything else the filesystem supplies.
//   - map iteration order: the sort is explicit and total.
//   - any OS-dependent separator: paths are normalised to forward slashes
//     before they reach here.
//
// Coverage is total. Every component passed in is hashed, with no "assets need
// not be covered" tier: a partial manifest would mean ADDING a file to a signed
// tree does not break verification, which is a laundering channel.
func Digest(components []Component) ([]byte, error) {
	lines := make([]string, 0, len(components))
	seen := make(map[string]struct{}, len(components))
	for _, c := range components {
		if err := validateDigestPath(c.Path); err != nil {
			return nil, err
		}
		if _, dup := seen[c.Path]; dup {
			return nil, fmt.Errorf("%w: duplicate component path %q", ErrBadPath, c.Path)
		}
		seen[c.Path] = struct{}{}
		sum := sha256.Sum256(c.Bytes)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+c.Path)
	}
	// Sort the rendered lines rather than the components: the hash prefix is
	// fixed-width, so line order is path order, and sorting the output is one
	// less place for a comparator to disagree with the emitted bytes.
	sort.Strings(lines)

	var buf bytes.Buffer
	buf.WriteString(DigestVersionMarker)
	buf.WriteByte('\n')
	for _, line := range lines {
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// validateDigestPath refuses paths the format cannot encode unambiguously.
//
// coreutils escapes backslashes and newlines in checksum files with a leading
// "\" on the line; rather than implement that escaping (and the matching
// un-escaping every consumer would then need) this refuses such paths outright.
// Fail loud: a path silently mangled into the digest is a path whose bytes are
// attested under the wrong name.
func validateDigestPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%w: empty path", ErrBadPath)
	case strings.ContainsAny(p, "\n\r\\"):
		return fmt.Errorf("%w: %q contains a newline or backslash, which the SHA256SUMS format cannot encode unescaped", ErrBadPath, p)
	case strings.HasPrefix(p, "/"):
		return fmt.Errorf("%w: %q is absolute, paths must be bundle-relative", ErrBadPath, p)
	case p == "." || p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") || strings.HasSuffix(p, "/.."):
		return fmt.Errorf("%w: %q escapes the bundle root", ErrBadPath, p)
	}
	return nil
}

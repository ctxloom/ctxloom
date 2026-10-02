package paths

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	// flatSep replaces every path separator in the readable tail, and
	// separates the tail from the hash.
	flatSep = "__"
	// flatTailMax bounds the readable tail in BYTES. Chosen so that a name
	// built on it — tail, separator, hash, then whatever a caller appends
	// (a nanosecond timestamp and extension for a record, ".lock" for a
	// lock, a collision counter, and the "." + name + ".<digits>.tmp" that
	// safefs.WriteFileKeepMode stages the write under) — stays well under the
	// 255-byte NAME_MAX every filesystem ctxloom runs on enforces.
	flatTailMax = 96
	// flatHashLen is the hex prefix of the sha256 kept: 64 bits, which is
	// collision-safe for the handful of paths one home directory names.
	flatHashLen = 16
)

// FlatName turns a path into ONE filename component of bounded length:
//
//	<readable tail>__<hash>
//
// where the tail is the last flatTailMax bytes of the path with every
// separator replaced by flatSep, and the hash is the first flatHashLen hex
// digits of the sha256 of the WHOLE slash-normalised path.
//
// Both halves are load-bearing; each was considered and rejected on its
// own. The tail alone — truncation — lets two deep paths that share a long
// suffix collide, and for a record store that means one writer
// silently reversing the other's application. The hash alone keeps a
// records or locks directory from being readable at a glance, so every tool
// that finds a file by name has to open it. Together: fixed bound whatever
// the depth, greppable by the file's own basename, and identity decided by
// the full path.
//
// The bound exists because a record or lock for a file nested inside a
// session's ephemeral directory would otherwise exceed NAME_MAX and fail
// with ENAMETOOLONG.
//
// Every character NTFS forbids in a name component (see ntfsReserved, plus
// control characters) is escaped in the tail as '%' and two uppercase hex
// digits, on EVERY OS, so a name means the same file wherever it was
// written. ':' is the load-bearing one: NTFS does not refuse "C:__proj..." as
// a name, it writes an alternate data stream of a file called "C", so a
// record or lock would save and never be found. '%' itself is NOT escaped,
// so the tail alone is not injective ("a:b" and a literal "a%3Ab" read
// alike); identity never rests on the tail, because the hash is taken over
// the UNESCAPED path.
//
// The encoding is total and deterministic: one path, however deep, always
// gets one name. Callers that need spellings of one file to agree (the lock
// paths do) resolve the path to a canonical form BEFORE calling this.
func FlatName(path string) string {
	slashed := filepath.ToSlash(path)
	sum := sha256.Sum256([]byte(slashed))
	hash := hex.EncodeToString(sum[:])[:flatHashLen]

	flat := escapeNTFSReserved(strings.ReplaceAll(slashed, "/", flatSep))
	if len(flat) > flatTailMax {
		flat = flat[len(flat)-flatTailMax:]
		// Cutting by BYTES can land inside a multi-byte rune; step forward to
		// the next rune start so the tail stays valid UTF-8.
		for len(flat) > 0 && !utf8.RuneStart(flat[0]) {
			flat = flat[1:]
		}
	}
	return flat + flatSep + hash
}

// ntfsReserved is every printable character NTFS refuses in a name component
// that the separator replacement does not already remove.
const ntfsReserved = `<>:"|?*`

// escapeNTFSReserved rewrites each NTFS-reserved or control character as
// '%' plus its two uppercase hex digits. All of them are ASCII, so the byte
// scan never splits a multi-byte rune.
func escapeNTFSReserved(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || strings.IndexByte(ntfsReserved, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

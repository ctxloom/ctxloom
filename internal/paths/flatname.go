package paths

import (
	"crypto/sha256"
	"encoding/hex"
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
	// agent.AtomicWriteFile stages the write under) — stays well under the
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
// The whole flattened path used to BE the name, which is why every write
// under an agent worktree — nested inside a session's ephemeral directory —
// failed with ENAMETOOLONG. Records already on disk under that scheme are
// renamed by their store on first read.
//
// The encoding is total and deterministic: one path, however deep, always
// gets one name. Callers that need spellings of one file to agree (the lock
// paths do) resolve the path to a canonical form BEFORE calling this.
//
// KNOWN GAP (inherited): on Windows an absolute path carries a drive letter
// (`C:\Users\...`), and `:` survives into the tail untouched. Left as-is
// rather than patched with logic this package's Unix-only test suite cannot
// exercise; a Windows-specific fix belongs beside a Windows-only file, with a
// test that actually runs there.
func FlatName(path string) string {
	slashed := filepath.ToSlash(path)
	sum := sha256.Sum256([]byte(slashed))
	hash := hex.EncodeToString(sum[:])[:flatHashLen]

	flat := strings.ReplaceAll(slashed, "/", flatSep)
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

package spool

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// A WAKE IS A NONCE ON DISK.
//
// The session owner's mail is delivered by its turn-start hook, and a wake is
// whatever makes a turn start when nobody is typing. The hook cannot tell a
// turn the wake caused from one a human caused unless the wake says so, and it
// cannot trust the wake's say-so unless the waker wrote it down first. So a
// wake is ARMED — a file in/wake/<nonce> — before it is fired, carries the
// nonce in its text (engine.WakeText), and is CONSUMED by the hook that sees that
// text. Consumption is the wake's acknowledgement: a nonce still on disk is a
// wake nobody has answered.
//
// in/wake/ is a sub-directory of in/, and nothing that reads in/ for MAIL sees
// it: Pending counts files, and a sweep skips directories. It is deliberately
// not a Dir at all — neither wire nor reader-local — because nothing in it is
// a message, and a scanner that enumerated it as one would report every armed
// wake as a malformed file.

// wakeDirName is the armed-wake directory, relative to the spool root.
const wakeDirName = "in/wake"

// nonceBytes is the nonce's entropy. Uniqueness among one harp's outstanding
// wakes is all it must provide.
const nonceBytes = 8

// nonceRE is the nonce grammar: exactly what ArmWake mints. A nonce arrives
// from a prompt — text anybody can type — so it is checked against this
// before it is ever joined to a path.
var nonceRE = regexp.MustCompile(`^[0-9a-f]{16}$`)

// ErrBadNonce refuses a nonce outside the minted grammar.
var ErrBadNonce = errors.New("spool: not a wake nonce")

// wakeDir is harp's armed-wake directory in m's view.
func wakeDir(m PathMapper, harp string) (string, error) {
	root, err := Root(m, harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, filepath.FromSlash(wakeDirName)), nil
}

// ArmWake mints a nonce and records it as an outstanding wake for harp. The
// file exists before the caller fires anything, so the hook that the wake
// causes always finds it — a wake fired first and recorded second races its
// own acknowledgement.
func ArmWake(m PathMapper, harp string) (string, error) {
	root, err := ensureRoot(m, harp)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, filepath.FromSlash(wakeDirName))
	if err := os.MkdirAll(dir, owneronly.DirMode); err != nil {
		return "", fmt.Errorf("spool: create %s: %w", dir, err)
	}
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("spool: minting a wake nonce: %w", err)
	}
	nonce := hex.EncodeToString(b)
	path := filepath.Join(dir, nonce)
	if err := iox.WriteFileAtomic(path, nil, owneronly.FileMode, iox.Durable()); err != nil {
		return "", fmt.Errorf("spool: arming wake %s: %w", path, err)
	}
	return nonce, nil
}

// ConsumeWake redeems nonce: true when it was outstanding for harp and is now
// consumed, false when it was not (already redeemed, never armed, or armed for
// another harp). A nonce outside the minted grammar is ErrBadNonce.
func ConsumeWake(m PathMapper, harp, nonce string) (bool, error) {
	if !nonceRE.MatchString(nonce) {
		return false, fmt.Errorf("%w: %q", ErrBadNonce, nonce)
	}
	dir, err := wakeDir(m, harp)
	if err != nil {
		return false, err
	}
	if err := os.Remove(filepath.Join(dir, nonce)); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("spool: consuming wake %s: %w", nonce, err)
	}
	return true, nil
}

// OutstandingWake lists harp's armed, unredeemed nonces, sorted. A spool that
// was never created has none.
func OutstandingWake(m PathMapper, harp string) ([]string, error) {
	dir, err := wakeDir(m, harp)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("spool: listing wakes in %s: %w", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && nonceRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// ClearWakes disarms every outstanding wake for harp and reports how many it
// cleared. The turn-start hook calls it on every turn that delivers mail:
// that turn delivered what every armed wake announced, and a nonce left on
// disk would refuse every later wake. A wake that lands after the clear finds
// no nonce and no mail, and the hook blocks it.
func ClearWakes(m PathMapper, harp string) (int, error) {
	out, err := OutstandingWake(m, harp)
	if err != nil {
		return 0, err
	}
	cleared := 0
	for _, nonce := range out {
		gone, err := ConsumeWake(m, harp, nonce)
		if err != nil {
			return cleared, err
		}
		if gone {
			cleared++
		}
	}
	return cleared, nil
}

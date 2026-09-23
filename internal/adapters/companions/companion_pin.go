package companions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// PinAdmittedCompanions admits every discovered companion and writes each
// admitted one — the exact bytes admitCompanion verified, and the signature
// that verified them — into a directory under storeRoot, returning that
// directory ("" when nothing was admitted).
//
// WHY IT EXISTS. Admission verifies ONE file, the one ctxloom's PATH resolved.
// But the hooks and MCP servers a companion's loadout contributes name it by
// its BARE name (agent.CtxloomCommand's invariant: an absolute path in a
// tracked settings file is one machine's fact every other clone inherits), and
// the engine resolves that name through ITS OWN PATH at every call. An
// unsigned binary of the same name earlier on that PATH — direnv,
// node_modules/.bin — would run as the pre-tool hook with no signature check.
// Putting this directory first on the engine's PATH makes the bare name reach
// the admitted bytes.
//
// COPIES, NOT LINKS: a link resolves at every call, so swapping the original
// after admission would swap what the hook runs. The cost of copying is
// bounded by content-addressing: the directory is named for the admitted set's
// digest, so every session with the same companions shares one copy, and a
// changed companion gets a new directory.
//
// The signature is copied too because a ctxloom started FROM this PATH — a hook
// the engine fires — discovers the pinned copy first and must admit it; the
// signature covers these bytes exactly, so it does.
func PinAdmittedCompanions(storeRoot string, root trust.TrustRoot) (string, error) {
	var names []string
	admitted := map[string]verifiedCompanion{}
	for _, bin := range DiscoverCompanions() { // sorted: the digest is stable
		a, v := admitCompanionVerified(bin, root)
		if !a.Allow {
			continue // admitCompanionVerified has already reported why
		}
		names = append(names, bin)
		admitted[bin] = v
	}
	if len(names) == 0 {
		return "", nil
	}
	h := sha256.New()
	for _, bin := range names {
		v := admitted[bin]
		p, s := sha256.Sum256(v.payload), sha256.Sum256(v.sig)
		fmt.Fprintf(h, "%s\x00%x\x00%x\n", bin, p, s)
	}
	dir := filepath.Join(storeRoot, hex.EncodeToString(h.Sum(nil)))
	if pinHolds(dir, admitted) {
		return dir, nil
	}
	if err := os.MkdirAll(storeRoot, 0o755); err != nil {
		return "", fmt.Errorf("companion pin store: %w", err)
	}
	tmp, err := os.MkdirTemp(storeRoot, ".pin-")
	if err != nil {
		return "", fmt.Errorf("companion pin store: %w", err)
	}
	defer os.RemoveAll(tmp) //nolint:errcheck // gone after a successful rename; best-effort otherwise
	for _, bin := range names {
		v := admitted[bin]
		if err := os.WriteFile(filepath.Join(tmp, bin), v.payload, 0o755); err != nil { //nolint:gosec // a companion must be executable
			return "", fmt.Errorf("pin companion %s: %w", bin, err)
		}
		if err := os.WriteFile(filepath.Join(tmp, bin+companionSigSuffix), v.sig, 0o644); err != nil { //nolint:gosec // a public signature
			return "", fmt.Errorf("pin companion %s signature: %w", bin, err)
		}
	}
	if err := os.Chmod(tmp, 0o755); err != nil { //nolint:gosec // a PATH directory
		return "", fmt.Errorf("companion pin store: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		// A concurrent launch pinned the same set first, or a directory under
		// this digest no longer holds it. Keep a good one; replace a bad one.
		if pinHolds(dir, admitted) {
			return dir, nil
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return "", fmt.Errorf("companion pin store: replace %s: %w", dir, errors.Join(err, rmErr))
		}
		if err := os.Rename(tmp, dir); err != nil {
			return "", fmt.Errorf("companion pin store: %w", err)
		}
	}
	return dir, nil
}

// pinHolds reports whether dir already holds exactly the admitted bytes for
// every name. The digest names the set, but the directory is only a file on
// disk, so a reused one is checked rather than believed.
func pinHolds(dir string, admitted map[string]verifiedCompanion) bool {
	for bin, v := range admitted {
		got, err := os.ReadFile(filepath.Join(dir, bin)) //nolint:gosec // a path this package named
		if err != nil || !bytes.Equal(got, v.payload) {
			return false
		}
		sig, err := os.ReadFile(filepath.Join(dir, bin+companionSigSuffix)) //nolint:gosec // a path this package named
		if err != nil || !bytes.Equal(sig, v.sig) {
			return false
		}
	}
	return true
}

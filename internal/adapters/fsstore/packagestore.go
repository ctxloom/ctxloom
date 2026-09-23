// Package fsstore is the filesystem adapter behind the core's session-state
// ports. Today it holds the claim store: the content-addressed package bytes
// a launch too large for the frame is stowed in, under the session dir both
// ends of the wire can reach.
package fsstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// ErrBadClaimLocation is Get's refusal of a location that is not one this
// store would have issued: a claim rides the wire, and a location that
// escapes the sessions root is a claim nobody stowed.
var ErrBadClaimLocation = errors.New("fsstore: a claim location must be <harp>/persist/package/<digest>")

// PackageStore is composite.Store over the sessions root. Put stows under
// ONE session (Harp), so a store that carries is rooted per launch; Get
// reads any location under Root, so a runner's store needs no harp of its
// own — the claim names it.
type PackageStore struct {
	// Root is the sessions root (<ctxloom home>/sessions) of THIS process:
	// the host's for the originator and a host runner, the mounted one for
	// a container runner.
	Root string
	// Harp is the session Put stows under. Empty on a store that only
	// redeems.
	Harp string
}

// SessionClaims is the claim-check transport over the package store rooted
// at one session: the per-session constructor a composition root hands the
// launch path (it has launch.SessionClaims' shape).
func SessionClaims(sessionsRoot, harp string) composite.Transport {
	return composite.ClaimCheck{Store: PackageStore{Root: sessionsRoot, Harp: harp}}
}

// Put writes the bytes at <Root>/<Harp>/persist/package/<hex digest> and
// returns that path relative to Root — a store-relative name, never a host
// path. A file already present under the digest is the same bytes by
// construction and is left alone.
func (s PackageStore) Put(_ context.Context, digest [32]byte, b []byte) (string, error) {
	if s.Harp == "" {
		return "", errors.New("fsstore: a package store that carries needs the session it stows under")
	}
	loc := filepath.ToSlash(filepath.Join(s.Harp, paths.PersistDirName, paths.PackageDirName, hex.EncodeToString(digest[:])))
	full := filepath.Join(s.Root, filepath.FromSlash(loc))
	if _, err := os.Stat(full); err == nil {
		return loc, nil
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("fsstore: stow package: %w", err)
	}
	if err := iox.WriteFileAtomic(full, b, 0o644, iox.AllowEmpty()); err != nil {
		return "", fmt.Errorf("fsstore: stow package: %w", err)
	}
	return loc, nil
}

// Get reads the bytes a claim names. nil bytes and no error for a location
// nothing was stowed at (composite.ClaimCheck refuses it as missing); a
// location of the wrong shape is refused here.
func (s PackageStore) Get(_ context.Context, location string) ([]byte, error) {
	if !validLocation(location) {
		return nil, fmt.Errorf("%w: %q", ErrBadClaimLocation, location)
	}
	b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(location)))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fsstore: redeem package: %w", err)
	}
	return b, nil
}

// validLocation is the shape Put issues: four clean segments, the third and
// fourth fixed, the last a hex digest.
func validLocation(loc string) bool {
	parts := strings.Split(loc, "/")
	if len(parts) != 4 || parts[1] != paths.PersistDirName || parts[2] != paths.PackageDirName {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, `\`) {
			return false
		}
	}
	_, err := hex.DecodeString(parts[3])
	return err == nil && len(parts[3]) == 64
}

var _ composite.Store = PackageStore{}

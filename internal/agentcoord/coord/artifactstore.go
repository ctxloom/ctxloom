package coord

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// artifactStoreDirName is the content-addressed blob store's subdirectory,
// alongside the journals in the project's state dir (statedir.go):
//
//	~/.ctxloom/coord/<project-key>/artifacts/<sha256-hex>
//
// Content addressing IS the integrity claim (E1e): the file name is the hash
// of its own bytes, so a store read that returns bytes hashing to a
// DIFFERENT name is corruption, caught before any download places them
// (artifacts.go). It also makes re-uploads free dedupe (E1a's idempotency
// rule) and needs no journal of its own: unlike runs.jsonl/mailbox.jsonl,
// writes here are content-identical regardless of which process or run
// produced them, so no single-writer serialization is required — atomic
// temp-then-rename is sufficient even under concurrent uploads of the same
// content (see writeAtomic). This is why the store does NOT fight
// statedir.go's owner.pid lock: that lock exists for journals that are NOT
// safe to share across processes; content-addressed blobs are.
const artifactStoreDirName = "artifacts"

// artifactStore is the coordinator-side content-addressed blob store.
type artifactStore struct {
	dir string
}

// newArtifactStore opens (creating if needed) the blob store under stateDir.
func newArtifactStore(stateDir string) (*artifactStore, error) {
	dir := filepath.Join(stateDir, artifactStoreDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("coord: artifact store: %w", err)
	}
	return &artifactStore{dir: dir}, nil
}

// path returns the content-addressed path for a hex sha256.
func (s *artifactStore) path(shaHex string) string {
	return filepath.Join(s.dir, shaHex)
}

// errArtifactSHAMismatch is returned by writeAtomic when the caller's
// declared hash does not match what was actually written (E1e: the upload
// header's sha256 is a CLAIM, verified here, never trusted).
var errArtifactSHAMismatch = errors.New("coord: artifact content does not match its declared sha256")

// writeAtomic drains r, hashing as it goes, and stores the bytes under the
// hash it actually computed — content addressing by construction, so the
// stored name is never a caller-supplied value. If declaredSHA is non-empty
// it is checked against the computed hash BEFORE the file is published
// (temp file removed, errArtifactSHAMismatch returned) — this is the
// upload-side half of E1e's mandatory integrity rule; the coordinator's own
// hash is authoritative, never the uploader's declared one.
//
// Idempotent: if content already exists under its hash, the temp file is
// discarded and the existing one wins — safe under a concurrent upload of
// the SAME content (both writers produce byte-identical files; whichever
// rename lands last is indistinguishable from the other).
func (s *artifactStore) writeAtomic(r io.Reader, declaredSHA []byte) (shaHex string, size int64, err error) {
	tmp, err := os.CreateTemp(s.dir, ".upload-*")
	if err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath) // no-op once renamed away
	}()

	h := sha256.New()
	n, err := io.Copy(tmp, io.TeeReader(r, h))
	if err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: fsync: %w", err)
	}
	sum := h.Sum(nil)
	if len(declaredSHA) > 0 && !bytes.Equal(sum, declaredSHA) {
		return "", 0, errArtifactSHAMismatch
	}
	shaHex = hex.EncodeToString(sum)
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: close: %w", err)
	}
	final := s.path(shaHex)
	if _, statErr := os.Stat(final); statErr == nil {
		// Free dedupe: identical content already stored under this hash.
		return shaHex, n, nil
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: publish: %w", err)
	}
	return shaHex, n, nil
}

// open opens a stored blob for reading by its hex sha256 ("", os.ErrNotExist
// on a miss).
func (s *artifactStore) open(shaHex string) (*os.File, error) {
	return os.Open(s.path(shaHex))
}

package coord

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// artifactStoreDirName is the content-addressed blob store's subdirectory,
// alongside the journals in the root's state dir (statedir.go):
//
//	~/.ctxloom/coord/<project-key>/<root-harp>/artifacts/<sha256-hex>
//
// Content addressing IS the integrity claim (E1e): the file name is the hash
// of its own bytes, so a store read that returns bytes hashing to a
// DIFFERENT name is corruption. Nothing on the SERVER side re-hashes a blob
// on the way out — artifacts.go's DownloadArtifact streams the stored bytes
// and puts the manifest's sha256 in the header — so the catch happens at the
// RECEIVER: homeartifacts.go's Home.DownloadArtifact hashes as it streams and
// refuses to place a file whose content disagrees with that header. It also
// makes re-uploads free dedupe (E1a's idempotency
// rule) and needs no journal of its own: unlike the coordinator journals,
// writes here are content-identical regardless of which process or run
// produced them, so no single-writer serialization is required — atomic
// temp-then-rename is sufficient even under concurrent uploads of the same
// content (see writeAtomic). This is why the store does NOT fight
// the owner lock (claimOwner): that lock exists for journals that are NOT
// safe to share across processes; content-addressed blobs are.
const artifactStoreDirName = "artifacts"

// artifactStore is the coordinator-side content-addressed blob store. Every
// read and write goes through fs.
type artifactStore struct {
	fs  afero.Fs
	dir string
}

// newArtifactStore opens (creating if needed) the blob store under stateDir
// on fs.
func newArtifactStore(fs afero.Fs, stateDir string) (*artifactStore, error) {
	dir := filepath.Join(stateDir, artifactStoreDirName)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("coord: artifact store: %w", err)
	}
	return &artifactStore{fs: fs, dir: dir}, nil
}

// path returns the content-addressed path for a hex sha256.
func (s *artifactStore) path(shaHex string) string {
	return filepath.Join(s.dir, shaHex)
}

// errArtifactSHAMismatch is returned by writeAtomic when the caller's
// declared hash does not match what was actually written (E1e: the upload
// header's sha256 is a CLAIM, verified here, never trusted).
var errArtifactSHAMismatch = errors.New("coord: artifact content does not match its declared sha256")

// errArtifactSizeMismatch is returned by writeAtomic when the caller's
// declared size_bytes does not match what was actually written: sha256 is
// optional on the wire, so a truncated/short delivery with no
// declared hash would otherwise sail through as a "success" that silently
// contradicts the caller's own declared size.
var errArtifactSizeMismatch = errors.New("coord: artifact content does not match its declared size")

// errArtifactBadName is returned when a name handed to the store is not a
// content hash. The store's whole contract is "the file name IS
// the sha256 of its own bytes", so anything else is a malformed request, not
// a miss — and left unchecked it is a plain filepath.Join into whatever the
// caller supplied.
var errArtifactBadName = errors.New("coord: artifact name is not a lowercase sha256 hex digest")

// shaHexLen is the length of a sha256 rendered by hex.EncodeToString, which
// is the ONLY way a name is ever minted here.
const shaHexLen = sha256.Size * 2

// validShaHex reports whether name is exactly what hex.EncodeToString emits
// for a sha256: 64 lowercase hex digits. Deliberately not hex.DecodeString —
// that accepts any even length and both cases, so it would admit both a
// short name and an uppercase twin of an existing blob.
func validShaHex(name string) bool {
	if len(name) != shaHexLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// writeAtomic drains r, hashing as it goes, and stores the bytes under the
// hash it actually computed — content addressing by construction, so the
// stored name is never a caller-supplied value. If declaredSHA is non-empty
// it is checked against the computed hash BEFORE anything is named
// (errArtifactSHAMismatch); likewise a non-zero declaredSize against the
// byte count (errArtifactSizeMismatch, which exists because sha256 is
// optional on the wire, so a short delivery with no declared hash would
// otherwise be stored under its own honest hash). Either way a mismatched
// upload never earns a name in the store, and its temp file is removed.
// The coordinator's own hash is authoritative (E1e), never the uploader's.
//
// The blob is committed Durable(): the manifest that will reference it is
// fsynced by the journal before its own response returns, so without the
// directory sync the durable reference could outlive its referent across a
// crash — a manifest naming a blob whose rename never landed.
//
// It is committed AllowEmpty() too, and that is safe here in a way it is not
// for a mutable file: the name IS the hash of the content, so the only file
// an empty blob can ever replace is another empty blob. Without it, an empty
// upload racing an identical one past the existence check below would be
// refused by the empty-write guard.
//
// Idempotent: if content already exists under its hash, the temp file is
// discarded and the existing one wins, with no rename and no directory sync.
// A concurrent upload of the SAME content can still slip between that check
// and the rename; both writers produce byte-identical files, so whichever
// rename lands last is indistinguishable from the other.
func (s *artifactStore) writeAtomic(r io.Reader, declaredSHA []byte, declaredSize uint64) (shaHex string, size int64, err error) {
	af, err := safefs.NewAtomicFileIn(s.fs, s.dir, 0o600, safefs.Durable(), safefs.AllowEmpty())
	if err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(af, io.TeeReader(r, h))
	if err != nil {
		_ = af.Abort()
		return "", 0, fmt.Errorf("coord: artifact store: write: %w", err)
	}
	sum := h.Sum(nil)
	if len(declaredSHA) > 0 && !bytes.Equal(sum, declaredSHA) {
		_ = af.Abort()
		return "", 0, errArtifactSHAMismatch
	}
	if declaredSize != 0 && uint64(n) != declaredSize {
		_ = af.Abort()
		return "", 0, errArtifactSizeMismatch
	}
	shaHex = hex.EncodeToString(sum)
	final := s.path(shaHex)
	if _, statErr := s.fs.Stat(final); statErr == nil {
		_ = af.Abort()
		return shaHex, n, nil
	}
	if err := af.CommitAs(final); err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: publish: %w", err)
	}
	return shaHex, n, nil
}

// open opens a stored blob for reading by its hex sha256 (os.ErrNotExist on
// a miss, errArtifactBadName when the name is not a content hash at all —
// the store never joins an unvalidated name onto its directory).
func (s *artifactStore) open(shaHex string) (afero.File, error) {
	if !validShaHex(shaHex) {
		return nil, fmt.Errorf("%w: %q", errArtifactBadName, shaHex)
	}
	return s.fs.Open(s.path(shaHex))
}

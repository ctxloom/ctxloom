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

	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// artifactStoreDirName is the content-addressed blob store's subdirectory,
// alongside the journals in the project's state dir (statedir.go):
//
//	~/.ctxloom/coord/<project-key>/artifacts/<sha256-hex>
//
// Content addressing IS the integrity claim (E1e): the file name is the hash
// of its own bytes, so a store read that returns bytes hashing to a
// DIFFERENT name is corruption. Nothing on the SERVER side re-hashes a blob
// on the way out — artifacts.go's DownloadArtifact streams the stored bytes
// and puts the manifest's sha256 in the header — so the catch happens at the
// RECEIVER: homeartifacts.go's Home.DownloadArtifact hashes as it streams and
// refuses to place a file whose content disagrees with that header. It also
// makes re-uploads free dedupe (E1a's idempotency
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

// errArtifactSizeMismatch is returned by writeAtomic when the caller's
// declared size_bytes does not match what was actually written: sha256 is
// optional on the wire, so a truncated/short delivery with no
// declared hash would otherwise sail through as a "success" that silently
// contradicts the caller's own declared size.
var errArtifactSizeMismatch = errors.New("coord: artifact content does not match its declared size")

// syncArtifactDir fsyncs the store directory after a blob is published.
//
// Indirected for the same reason statedir.go's writeOwnerPID is: on a real
// filesystem this fails only on conditions a test cannot provoke, while the
// consequence of skipping it — a durable manifest naming a blob whose rename
// never landed — is precisely what must not happen.
//
// Points at iox.SyncDir, the shared "open dir, fsync, close" primitive
// (formerly this package's own fsyncDir/artifactstore_dirsync*.go, byte-for-
// byte the same operation with no tolerance quirks of its own — collapsed
// per taskloom unbounded-bacon's Durable() ruling). Still swappable here for
// artifactstore_dirsync_test.go, which needs to observe the seam and inject
// failures the way iox's own tests do internally.
var syncArtifactDir = iox.SyncDir

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
// it is checked against the computed hash BEFORE the file is published
// (temp file removed, errArtifactSHAMismatch returned) — this is the
// upload-side half of E1e's mandatory integrity rule; the coordinator's own
// hash is authoritative, never the uploader's declared one. Likewise, if
// declaredSize is non-zero it is checked against the actual byte count
// BEFORE publish (errArtifactSizeMismatch) — both checks exist so a
// mismatched upload never earns a name in the store, matching or not.
//
// Idempotent: if content already exists under its hash, the temp file is
// discarded and the existing one wins — safe under a concurrent upload of
// the SAME content (both writers produce byte-identical files; whichever
// rename lands last is indistinguishable from the other).
func (s *artifactStore) writeAtomic(r io.Reader, declaredSHA []byte, declaredSize uint64) (shaHex string, size int64, err error) {
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
	// The size cross-check lives HERE, before publish, for the
	// same reason the sha check does — a declared size that does not match
	// what was actually received must never earn a name in the
	// content-addressed store, or a truncated/short delivery would be
	// published (under its own, honest hash) and still leave an orphan
	// blob behind even though the caller's receipt was refused.
	if declaredSize != 0 && uint64(n) != declaredSize {
		return "", 0, errArtifactSizeMismatch
	}
	shaHex = hex.EncodeToString(sum)
	if err := tmp.Close(); err != nil {
		return "", 0, fmt.Errorf("coord: artifact store: close: %w", err)
	}
	if err := s.publish(tmpPath, shaHex); err != nil {
		return "", 0, err
	}
	return shaHex, n, nil
}

// publish moves a fully drained, verified temp file into place under its
// content hash and makes that name durable.
//
// The blob's CONTENT is already durable (writeAtomic's tmp.Sync) but its
// NAME is not until the directory entry is fsynced — and the manifest that
// will reference this blob IS fsynced, by the journal, before its own
// response returns (journal.go's Store.execLocked). Without the directory
// sync the durable reference can outlive its referent across a crash: a
// manifest naming a blob whose rename never landed, answering NOT_FOUND for
// the rest of its life.
func (s *artifactStore) publish(tmpPath, shaHex string) error {
	final := s.path(shaHex)
	if _, statErr := os.Stat(final); statErr == nil {
		// Free dedupe: identical content already stored under this hash, and
		// no new directory entry to make durable.
		return nil
	}
	if err := os.Rename(tmpPath, final); err != nil {
		return fmt.Errorf("coord: artifact store: publish: %w", err)
	}
	if err := syncArtifactDir(s.dir); err != nil {
		return fmt.Errorf("coord: artifact store: fsync dir: %w", err)
	}
	return nil
}

// open opens a stored blob for reading by its hex sha256 (os.ErrNotExist on
// a miss, errArtifactBadName when the name is not a content hash at all —
// the store never joins an unvalidated name onto its directory).
func (s *artifactStore) open(shaHex string) (*os.File, error) {
	if !validShaHex(shaHex) {
		return nil, fmt.Errorf("%w: %q", errArtifactBadName, shaHex)
	}
	return os.Open(s.path(shaHex))
}

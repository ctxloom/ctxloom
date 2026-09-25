package coord

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// Artifact transfer, the coordinator's half: the upload is a declared header
// and a body stream the store writes atomically under its content address;
// the download is an authorized manifest lookup and the stored blob. The
// chunked wire shape is the adapter's; the content-addressed store itself
// lives in artifactstore.go.

// ArtifactChunkCap bounds one upload/download chunk, well under the 4 MiB
// default gRPC frame cap.
const ArtifactChunkCap = 1 << 20

// ArtifactUploadSizeCap bounds one artifact's total size — runner-read,
// size-capped sanely. 64 MiB comfortably covers plan/report/dataset
// artifacts without risking runaway memory/disk from a misbehaving caller.
const ArtifactUploadSizeCap = 64 << 20

// ArtifactUpload is an upload's declared header: the run it belongs to (must
// be the uploading credential's), the artifact's identity and its declared
// size and digest, which the store verifies against the bytes received.
type ArtifactUpload struct {
	RunID      string
	ArtifactID string
	Name       string
	MediaType  string
	SizeBytes  uint64
	SHA256     []byte
}

// ArtifactReceipt is a stored upload: the id later manifests reference
// (UploadID = hex(sha256)), the server-computed digest and size.
type ArtifactReceipt struct {
	UploadID  string
	SHA256    []byte
	SizeBytes uint64
	StoredAt  time.Time
}

// ErrArtifactSHAMismatch refuses an upload whose bytes do not hash to the
// declared sha256.
var ErrArtifactSHAMismatch = errArtifactSHAMismatch

// ErrArtifactSizeMismatch refuses an upload whose bytes do not add up to the
// declared size.
var ErrArtifactSizeMismatch = errArtifactSizeMismatch

// ReceiveArtifact stores one uploaded artifact body under its content
// address. The caller's credential must own the run the header names and
// must not be a read-only consumer (ErrForbidden); the declared size must be
// within the cap and non-zero (ErrInvalidRequest — an empty artifact is a
// receipt for nothing). body is read to EOF; a body that does not match the
// declared digest or size is refused after the read (ErrArtifactSHAMismatch,
// ErrArtifactSizeMismatch — both ErrInvalidRequest too).
func (c *Coordinator) ReceiveArtifact(caller Identity, h ArtifactUpload, body io.Reader) (ArtifactReceipt, error) {
	if h.ArtifactID == "" {
		return ArtifactReceipt{}, Refusal(ErrInvalidRequest, "upload: artifact_id is required")
	}
	if caller.Consumer {
		return ArtifactReceipt{}, Refusal(ErrForbidden, "upload: a read-only consumer credential cannot upload")
	}
	if caller.RunID != h.RunID {
		return ArtifactReceipt{}, Refusal(ErrForbidden, "upload: run_id %q does not match this connection's credential", h.RunID)
	}
	if h.SizeBytes > ArtifactUploadSizeCap {
		return ArtifactReceipt{}, Refusal(ErrInvalidRequest, "upload: declared size %d exceeds the %d-byte cap", h.SizeBytes, ArtifactUploadSizeCap)
	}
	if h.SizeBytes == 0 {
		return ArtifactReceipt{}, Refusal(ErrInvalidRequest, "upload: declared size is 0 — an empty artifact is a receipt for nothing, refusing it")
	}
	shaHex, size, err := c.artifacts.writeAtomic(body, h.SHA256, h.SizeBytes)
	if err != nil {
		switch {
		case errors.Is(err, errArtifactSHAMismatch):
			return ArtifactReceipt{}, Refusal(ErrArtifactSHAMismatch, "upload: received content (sha256 %s) does not match the declared sha256", shaHex)
		case errors.Is(err, errArtifactSizeMismatch):
			return ArtifactReceipt{}, Refusal(ErrArtifactSizeMismatch, "upload: declared size %d does not match the bytes actually received", h.SizeBytes)
		}
		return ArtifactReceipt{}, fmt.Errorf("upload: %w", err)
	}
	c.audit("artifact.uploaded", caller.Harp, map[string]string{
		"run_id":      h.RunID,
		"artifact_id": h.ArtifactID,
		"sha256":      shaHex,
		"size_bytes":  fmt.Sprint(size),
	})
	shaBytes, _ := hex.DecodeString(shaHex) // shaHex is our own hex.EncodeToString output
	return ArtifactReceipt{UploadID: shaHex, SHA256: shaBytes, SizeBytes: uint64(size), StoredAt: c.now()}, nil
}

// authorizeArtifactDownload allows the owner, its parent, and any consumer
// credential (read-only viewers may read).
func (c *Coordinator) authorizeArtifactDownload(caller Identity, ownerHarp string) error {
	if caller.Consumer || caller.Harp == ownerHarp {
		return nil
	}
	allowed := false
	c.runs.View(func() {
		if r := c.runsF.currentRun(ownerHarp); r != nil && r.ParentHarp == caller.Harp {
			allowed = true
		}
	})
	if allowed {
		return nil
	}
	return Refusal(ErrForbidden, "download: %q is not this session, its child, or a consumer credential", ownerHarp)
}

// OpenArtifact resolves and opens one stored artifact for download: the
// manifest (latest revision) and the blob, positioned at offset. Refusals:
// ErrInvalidRequest (a missing id, an offset past the end), ErrForbidden (the
// caller may not read ownerHarp's artifacts), ErrNotFound (no such manifest,
// or its content is gone). The caller closes the blob.
func (c *Coordinator) OpenArtifact(caller Identity, ownerHarp, artifactID string, offset uint64) (ArtifactRecord, *os.File, error) {
	if err := requireArtifactAddress(ownerHarp, artifactID); err != nil {
		return ArtifactRecord{}, nil, err
	}
	if err := c.authorizeArtifactDownload(caller, ownerHarp); err != nil {
		return ArtifactRecord{}, nil, err
	}
	rec, ok := c.artifactRecord(ownerHarp, artifactID)
	if !ok {
		return ArtifactRecord{}, nil, Refusal(ErrNotFound, "download: no artifact %q for %q", artifactID, ownerHarp)
	}
	if offset > 0 && offset >= rec.SizeBytes {
		return ArtifactRecord{}, nil, Refusal(ErrInvalidRequest, "download: offset %d is past the end of %q (%d bytes)", offset, artifactID, rec.SizeBytes)
	}
	if _, err := hex.DecodeString(rec.SHA256); err != nil {
		return ArtifactRecord{}, nil, fmt.Errorf("download: corrupt manifest sha256 for %q: %v", artifactID, err)
	}
	f, err := c.openArtifactAt(rec, artifactID, offset)
	if err != nil {
		return ArtifactRecord{}, nil, err
	}
	return rec, f, nil
}

// requireArtifactAddress refuses a download that names no owner or no
// artifact.
func requireArtifactAddress(ownerHarp, artifactID string) error {
	if ownerHarp == "" {
		return Refusal(ErrInvalidRequest, "download: agent_id is required")
	}
	if artifactID == "" {
		return Refusal(ErrInvalidRequest, "download: artifact_id is required")
	}
	return nil
}

// openArtifactAt opens rec's stored content positioned at offset. A bad
// stored name is a corrupt manifest; absent content is not found.
func (c *Coordinator) openArtifactAt(rec ArtifactRecord, artifactID string, offset uint64) (*os.File, error) {
	f, err := c.artifacts.open(rec.SHA256)
	if err != nil {
		if errors.Is(err, errArtifactBadName) {
			return nil, fmt.Errorf("download: corrupt manifest sha256 for %q: %v", artifactID, err)
		}
		return nil, Refusal(ErrNotFound, "download: stored content missing for %q: %v", artifactID, err)
	}
	if offset == 0 {
		return f, nil
	}
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		_ = f.Close()
		return nil, Refusal(ErrInvalidRequest, "download: seek to offset %d: %v", offset, err)
	}
	return f, nil
}

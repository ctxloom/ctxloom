package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"path/filepath"
	"slices"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
	"github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

// errStaleWatermark reports that a watermark no longer describes the files it
// was taken over. It is never surfaced: the answer is a full rebuild.
var errStaleWatermark = errors.New("transcript watermark no longer describes the files it was taken over")

// transcriptWatermark is where a refresh of a harp's canonical transcript
// resumes from (paths.ResolveHarpSegmentWatermarkPath, keyed by the live
// binding's session id). It is the ONLY state incremental conversion keeps,
// written by the rebuild that consumed the records, after the canonical file
// it describes is committed.
//
// It is SELF-VALIDATING rather than kept in step: it is a separate file from
// the canonical transcript, so the two cannot be committed atomically, and a
// crash, a hand edit or a full rebuild can leave it describing something
// else. Every field below is checked before it is trusted, and any mismatch
// is a full rebuild — today's behavior, so a bad watermark costs time, never
// correctness.
type transcriptWatermark struct {
	// Source is the live vendor file the checkpoint was taken in.
	Source string `json:"source"`
	// Rotations are the session ids of the rotation segments the canonical
	// prefix was built from, oldest first; a different lineage is a
	// different prefix.
	Rotations []string `json:"rotations"`
	// CanonicalLength and CanonicalSHA256 identify the canonical bytes the
	// checkpoint corresponds to: everything before the provisional tail.
	CanonicalLength int64  `json:"canonical_length"`
	CanonicalSHA256 string `json:"canonical_sha256"`
	// NextSeq and SessionID are where the canonical lines continue
	// (transcript.WithContinuation).
	NextSeq   int    `json:"next_seq"`
	SessionID string `json:"session_id,omitempty"`
	// Vendor is the adapter's own checkpoint in Source.
	Vendor vendorreader.Checkpoint `json:"vendor"`
}

// rotationIDs is e's rotation lineage as a watermark records it.
func rotationIDs(e sessions.Entry) []string {
	ids := make([]string, len(e.Rotations))
	for i, r := range e.Rotations {
		ids[i] = r.SessionID
	}
	return ids
}

// loadWatermark returns e's watermark when there is one this rebuild could
// resume from: a resumable adapter and a watermark taken over this same live
// source and lineage (one is only ever written for a keyed live binding —
// saveWatermark — so an unbound or unkeyed entry finds none). The canonical-bytes and
// vendor-line checks happen while resuming (copyCanonicalPrefix,
// vendorreader.Checkpoint.Seek), where the bytes are read anyway.
func loadWatermark(fsys afero.Fs, adapter vendorreader.VendorAdapter, e sessions.Entry, liveSrc string) (vendorreader.ResumableAdapter, *transcriptWatermark, bool) {
	ra, resumable := adapter.(vendorreader.ResumableAdapter)
	if !resumable {
		return nil, nil, false
	}
	p, err := paths.ResolveHarpSegmentWatermarkPath(e.HarpName, e.SessionID)
	if err != nil {
		return nil, nil, false
	}
	raw, err := afero.ReadFile(fsys, p)
	if err != nil {
		return nil, nil, false
	}
	var wm transcriptWatermark
	if json.Unmarshal(raw, &wm) != nil || wm.Source != liveSrc || !slices.Equal(wm.Rotations, rotationIDs(e)) {
		return nil, nil, false
	}
	return ra, &wm, true
}

// saveWatermark records wm as e's watermark, or removes e's watermark when
// wm is nil (the rebuild offered no checkpoint, so whatever is there
// describes an older transcript). A failure is warned about, never returned:
// the transcript it describes is already committed, and a missing or stale
// watermark only costs the next refresh a full conversion.
func saveWatermark(fsys afero.Fs, e sessions.Entry, wm *transcriptWatermark) {
	if e.SessionID == "" {
		return
	}
	p, err := paths.ResolveHarpSegmentWatermarkPath(e.HarpName, e.SessionID)
	if err == nil {
		err = writeWatermarkFile(fsys, p, wm)
	}
	if err != nil {
		clidiag.Warn("ctxloom", "rebuild %s: could not record the transcript watermark (%v); the next refresh will convert in full", e.HarpName, err)
	}
}

func writeWatermarkFile(fsys afero.Fs, p string, wm *transcriptWatermark) error {
	if wm == nil {
		if err := fsys.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	raw, err := json.Marshal(wm)
	if err != nil {
		return err
	}
	if err := fsys.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return safefs.WriteFile(fsys, p, raw, 0o644)
}

// copyCanonicalPrefix copies the first wm.CanonicalLength bytes of the
// canonical transcript at dest onto w and verifies they are the bytes the
// watermark was taken over. Raw bytes, no decoding: this is what a resume
// pays for the prefix instead of re-converting it.
func copyCanonicalPrefix(fsys afero.Fs, dest string, wm *transcriptWatermark, w io.Writer) error {
	digest := sha256.New()
	f, err := fsys.Open(dest)
	if err != nil {
		return fmt.Errorf("%w: %w", errStaleWatermark, err)
	}
	defer func() { _ = f.Close() }()
	// A canonical file shorter than the watermark is io.EOF here, and then
	// the digest of what was copied cannot match.
	if _, err := io.CopyN(io.MultiWriter(w, digest), f, wm.CanonicalLength); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("copy canonical prefix: %w", err)
	}
	if hex.EncodeToString(digest.Sum(nil)) != wm.CanonicalSHA256 {
		return errStaleWatermark
	}
	return nil
}

// resumePoint is where a live conversion starts: the vendor checkpoint and
// where the canonical lines continue — the zero vendor checkpoint and Seq 0
// for a conversion from the beginning.
type resumePoint struct {
	vendor    vendorreader.Checkpoint
	seq       int
	sessionID string
}

// rebuildFile is a canonical rebuild in progress. Every byte reaches the
// AtomicFile through Write, which also counts and digests it, so the
// watermark and the nothing-written check read these instead of the temp
// file.
type rebuildFile struct {
	af      *safefs.AtomicFile
	digest  hash.Hash
	written int64
}

func newRebuildFile(fsys afero.Fs, path string) (*rebuildFile, error) {
	af, err := safefs.NewAtomicFile(fsys, path, 0o644)
	if err != nil {
		return nil, err
	}
	return &rebuildFile{af: af, digest: sha256.New()}, nil
}

// Write appends p to the AtomicFile, counting and digesting what it accepted
// — a short write included, so the digest always matches the file.
func (f *rebuildFile) Write(p []byte) (int, error) {
	n, err := f.af.Write(p)
	f.digest.Write(p[:n])
	f.written += int64(n)
	return n, err
}

// convertLive converts liveSrc onto rf from `from`, returning the watermark
// taken at the adapter's checkpoint — nil when it offered none, which a
// non-resumable adapter never does.
func convertLive(ctx context.Context, fsys afero.Fs, adapter vendorreader.VendorAdapter, e sessions.Entry, rf *rebuildFile, liveSrc string, from resumePoint) (*transcriptWatermark, error) {
	rec, err := transcript.NewRecorder(fsys, e.HarpName, e.Backend, transcript.WithWriter(rf),
		transcript.WithClock(vendorSourceClock(fsys, liveSrc)), transcript.WithContinuation(from.seq, from.sessionID))
	if err != nil {
		return nil, fmt.Errorf("open recorder for %s: %w", e.HarpName, err)
	}
	tr := &trackingRecorder{Recorder: rec, seq: from.seq, sessionID: from.sessionID}
	var wm *transcriptWatermark
	if ra, ok := adapter.(vendorreader.ResumableAdapter); ok {
		err = ra.ConvertFrom(ctx, fsys, tr, liveSrc, from.vendor, func(cp vendorreader.Checkpoint) error {
			wm = takeWatermark(rf, tr, cp)
			return nil
		})
	} else {
		err = adapter.Convert(ctx, fsys, tr, liveSrc)
	}
	_ = rec.Close()
	if err != nil {
		return nil, fmt.Errorf("convert %s transcript for %s: %w", e.Backend, e.HarpName, err)
	}
	if wm != nil {
		wm.Source, wm.Rotations = liveSrc, rotationIDs(e)
	}
	return wm, nil
}

// takeWatermark is the watermark at checkpoint cp: the canonical bytes
// written to the rebuild so far, and where the canonical lines continue.
func takeWatermark(rf *rebuildFile, tr *trackingRecorder, cp vendorreader.Checkpoint) *transcriptWatermark {
	return &transcriptWatermark{
		CanonicalLength: rf.written,
		CanonicalSHA256: hex.EncodeToString(rf.digest.Sum(nil)),
		NextSeq:         tr.seq,
		SessionID:       tr.sessionID,
		Vendor:          cp,
	}
}

// trackingRecorder follows the two things a watermark needs to say about the
// canonical lines written so far — the next Seq and the session id lines
// carry — by watching what passes through to the Recorder that stamps them.
type trackingRecorder struct {
	transcript.Recorder
	seq       int
	sessionID string
}

func (r *trackingRecorder) Record(ev agent.ChatEvent) error {
	if err := r.Recorder.Record(ev); err != nil {
		return err
	}
	r.seq++
	if ev.Session != nil && ev.Session.SessionID != "" {
		r.sessionID = ev.Session.SessionID
	}
	return nil
}

// newResumePoint is a conversion from the beginning.
func newResumePoint() resumePoint { return resumePoint{} }

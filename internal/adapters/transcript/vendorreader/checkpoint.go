package vendorreader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ctxloom/ctxloom/internal/adapters/transcript"
)

// ErrCheckpointMismatch reports that a vendor file no longer extends the
// checkpoint a conversion was asked to resume from — truncated, rewritten, or
// a different file at the same path — or that the checkpoint's adapter state
// is unreadable. It is not a conversion failure: the caller's answer is a full
// conversion from the beginning.
var ErrCheckpointMismatch = errors.New("vendor transcript no longer extends the checkpoint it would resume from")

// Checkpoint is where a resumable conversion of one vendor file picks up
// again. Every line ending before Offset has been converted and is never read
// again; State is the adapter's own cross-line state as of that point (an
// open turn boundary, the counters its end-of-file checks need), opaque to
// everyone but the adapter that wrote it.
//
// The checkpointed line itself — [Start, Offset) — is kept as a digest and
// re-read on resume. That one line is how a resume tells "the same file,
// appended to" from anything else at the same path, without reading the
// prefix it exists to skip.
type Checkpoint struct {
	Offset     int64           `json:"offset"`
	Start      int64           `json:"line_start"`
	LineSHA256 string          `json:"line_sha256"`
	State      json.RawMessage `json:"state"`
}

// NewCheckpoint is the checkpoint after l, a TERMINATED line (an unterminated
// one may still be mid-write), carrying the adapter's state as of l.
func NewCheckpoint(l Line, state json.RawMessage) Checkpoint {
	return Checkpoint{Offset: l.End, Start: l.Start, LineSHA256: lineDigest(l.Bytes), State: state}
}

// Seek positions r at cp.Offset, having verified that r still holds the
// checkpointed line there. The zero Checkpoint seeks to the beginning.
// ErrCheckpointMismatch when the verification fails; any other error is r's.
func (cp Checkpoint) Seek(r io.ReadSeeker) error {
	if cp.Offset == 0 {
		_, err := r.Seek(0, io.SeekStart)
		return err
	}
	if cp.Start < 0 || cp.Start >= cp.Offset {
		return ErrCheckpointMismatch
	}
	if _, err := r.Seek(cp.Start, io.SeekStart); err != nil {
		return err
	}
	raw := make([]byte, cp.Offset-cp.Start)
	if _, err := io.ReadFull(r, raw); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return ErrCheckpointMismatch
		}
		return fmt.Errorf("read checkpointed line: %w", err)
	}
	if raw[len(raw)-1] != '\n' || lineDigest(bytes.TrimSpace(raw)) != cp.LineSHA256 {
		return ErrCheckpointMismatch
	}
	return nil
}

func lineDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ResumableAdapter is a VendorAdapter that can convert a vendor file from a
// Checkpoint forward, so a still-growing session is not re-read from its
// beginning on every refresh. An adapter that is not one is always converted
// in full; nothing else changes.
type ResumableAdapter interface {
	VendorAdapter
	// ConvertFrom converts src from `from` (the zero Checkpoint: from the
	// beginning, recording exactly what Convert would) and calls onCheckpoint
	// AT MOST ONCE: after every record of the last newline-terminated line,
	// before anything recorded from an unterminated line or flushed at end of
	// file — those are provisional, re-derived by the next resume. It is not
	// called when the adapter cannot resume from this conversion (its
	// one-time session header is not settled yet); a resume that reached no
	// new terminated line hands back `from` itself.
	//
	// ErrCheckpointMismatch when src no longer extends `from`; otherwise the
	// VendorAdapter error contract.
	ConvertFrom(ctx context.Context, rec transcript.Recorder, src string, from Checkpoint, onCheckpoint func(Checkpoint) error) error
}

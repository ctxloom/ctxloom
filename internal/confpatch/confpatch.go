// Package confpatch writes ctxloom's changes into config files ctxloom does
// not own, and remembers exactly what it wrote so the next write can take it
// back out.
//
// The loop, per target, is:
//
//  1. find what ctxloom PREVIOUSLY applied to this target (its record),
//  2. apply that record's REVERSAL, restoring the user's bytes exactly,
//  3. apply the newly computed set,
//  4. write one new record.
//
// One computation, one apply, one record per target.
//
// The reversal is DIFFED from the before- and after-images rather than reasoned
// backwards from the ops that produced them. That is deliberate and it is what
// makes this possible today: hew's spec (§9.7) defers `hew revert` and its
// inversion rules — "what is the inverse of an `add` with `on_conflict: keep`?"
// — as an unsettled design task, while the diff-both-images half needs no
// inversion rules at all because both images existed. hew.Invert is that half.
//
// What this replaces, and why each alternative is worse, is recorded on the
// taskloom task this implements: a `_ctxloom` marker written into the user's
// file is pollution in a file ctxloom does not own and breaks the moment a user
// copies an entry; enumerating the file to find ctxloom's entries asks the
// foreign file a question only ctxloom's own memory can answer; a ledger of
// managed NAMES knows names, not values, so it cannot restore a value it
// replaced.
package confpatch

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Store is the §9.7 application-record store. Records are home-rooted (see
// paths.HomeRecordsDir) for the same reason filelock's lock sidecars are: the
// target file is FOREIGN, ctxloom does not own it, and so must never leave its
// own state beside it.
type Store struct {
	fs  afero.Fs
	dir string
}

// NewStore opens the record store at dir on fs. dir is created lazily, on the
// first record written, so merely constructing a Store touches no disk.
func NewStore(recordFS afero.Fs, dir string) (*Store, error) {
	if recordFS == nil {
		return nil, errors.New("confpatch: nil record filesystem")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("confpatch: empty record directory")
	}
	return &Store{fs: recordFS, dir: dir}, nil
}

// Result reports what one Apply did.
type Result struct {
	// RecordPath is the §9.7 record just written.
	RecordPath string
	// Before is the target's bytes as found, BEFORE the reversal of any prior
	// application — the user's file as it stood on disk.
	Before []byte
	// Restored is Before with the prior application reversed: the user's own
	// content, with ctxloom's previous entries removed. It equals Before when
	// there was no prior record.
	Restored []byte
	// After is what was written.
	After []byte
	// Reversed reports whether a prior record was found and reversed.
	Reversed bool
	// Changed reports whether After differs from Before.
	Changed bool
}

// Apply runs the loop against one target: reverse what ctxloom applied last
// time, apply want, and write one record describing it.
//
// want is a hew.TransformList — the caller builds its own ops. Callers that
// find that tedious should NOT reach around this function to write the file
// directly; the record is the whole point, and a write without one is the
// silent no-op this package exists to prevent.
//
// Nothing is written unless every step succeeds. In particular, a reversal that
// will not apply FAILS the whole call rather than being skipped: a reversal
// that no longer fits means the user edited the region ctxloom manages, and
// applying the new set on top of that would clobber their edit. Refusing is the
// documented behaviour hew's own staleness guard exists to produce.
func (s *Store) Apply(targetFS afero.Fs, target string, want hew.TransformList) (Result, error) {
	var res Result

	if targetFS == nil {
		return res, errors.New("confpatch: nil target filesystem")
	}
	if strings.TrimSpace(target) == "" {
		return res, errors.New("confpatch: empty target path")
	}

	format, ok := hew.DetectFormat(filepath.Base(target))
	if !ok {
		return res, fmt.Errorf("confpatch: hew does not recognize %s's format from its name, so it cannot be patched byte-preservingly", target)
	}
	binding, ok := hew.Lookup(format)
	if !ok || binding.Applier == nil {
		return res, fmt.Errorf("confpatch: this build has no hew applier for %q, needed to write %s", format, target)
	}
	if binding.Document == nil {
		// A half binding can apply but not read, and a record needs the read
		// side. Refuse rather than write a record that states nothing.
		return res, fmt.Errorf("confpatch: this build has no hew document reader for %q, so a write to %s could not be recorded", format, target)
	}

	err := agent.WithFileLock(targetFS, target, func() error {
		before, existed, err := readTarget(targetFS, target)
		if err != nil {
			return err
		}
		if !existed {
			before = emptyDocument(format)
		}
		res.Before = before

		// 1+2. Reverse what ctxloom applied here last time.
		restored := before
		prev, found, err := s.Last(target)
		if err != nil {
			return err
		}
		if found && len(prev.Reversal) > 0 {
			restored, err = applyPatchText(binding, before, prev.Reversal, target)
			if err != nil {
				return fmt.Errorf("confpatch: %s has drifted since ctxloom last wrote it, so the previous application could not be reversed; refusing to write rather than clobber the change: %w", target, err)
			}
			res.Reversed = true
		}
		res.Restored = restored

		// 3. Apply the newly computed set.
		after, err := binding.Applier(restored, want)
		if err != nil {
			return fmt.Errorf("confpatch: apply ctxloom's changes to %s: %w", target, err)
		}
		res.After = after
		res.Changed = string(after) != string(before)

		// 4. Derive this application's reversal from the two images, render it
		// as patch text, and write the record BEFORE the target: a record
		// describing a write that then fails is recoverable noise, whereas a
		// write with no record is exactly the ownership gap this closes.
		reversal, err := renderReversal(format, restored, after, target)
		if err != nil {
			return err
		}
		recordPath, err := s.write(target, format, want, restored, after, reversal)
		if err != nil {
			return err
		}
		res.RecordPath = recordPath

		if err := writeTarget(targetFS, target, after); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// renderReversal diffs after back to restored and renders the result as .hew
// patch text — the durable artifact the record keeps.
//
// Preamble is REQUIRED, not cosmetic: hew's parser refuses a document without
// the "hew: 1" line (§2.1), so a reversal rendered without it is unparseable
// and the record would carry an undo nothing can apply. Rendering it here and
// re-parsing it in applyPatchText is what proves the record's reversal is
// usable at the moment it is written rather than years later when it is needed.
func renderReversal(format hew.FormatID, before, after []byte, target string) ([]byte, error) {
	tl, err := hew.Invert(format, before, after, hew.DiffOptions{Target: target})
	if err != nil {
		return nil, fmt.Errorf("confpatch: derive how to undo the write to %s: %w", target, err)
	}
	out, err := hew.Render(tl, hew.RenderOptions{Preamble: true})
	if err != nil {
		return nil, fmt.Errorf("confpatch: render the reversal of the write to %s: %w", target, err)
	}
	return out, nil
}

// applyPatchText parses stored .hew text and applies it. Parsing at APPLY time,
// from the same bytes the record holds, is what makes the stored reversal a
// real artifact rather than a description of one.
func applyPatchText(b hew.Binding, src, patch []byte, target string) ([]byte, error) {
	tl, err := hew.ParseSingle(patch)
	if err != nil {
		return nil, fmt.Errorf("parse the recorded reversal for %s: %w", target, err)
	}
	out, err := b.Applier(src, tl)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// emptyDocument is a format's empty document, used as the pre-image when the
// target does not exist yet. Format-specific and deliberately not one shared
// literal: "{}" is an empty JSON object, but in TOML it is a parse error —
// an empty TOML document is zero bytes.
func emptyDocument(format hew.FormatID) []byte {
	switch format {
	case hew.FormatJSON, hew.FormatJSONC:
		return []byte("{}")
	default:
		return nil
	}
}

func readTarget(fs afero.Fs, target string) ([]byte, bool, error) {
	exists, err := afero.Exists(fs, target)
	if err != nil {
		return nil, false, fmt.Errorf("confpatch: stat %s: %w", target, err)
	}
	if !exists {
		return nil, false, nil
	}
	data, err := afero.ReadFile(fs, target)
	if err != nil {
		return nil, false, fmt.Errorf("confpatch: read %s: %w", target, err)
	}
	return data, true, nil
}

func writeTarget(fs afero.Fs, target string, out []byte) error {
	if dir := filepath.Dir(target); dir != "." {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("confpatch: create directory for %s: %w", target, err)
		}
	}
	if err := agent.AtomicWriteFile(fs, target, out, filepath.Base(target)); err != nil {
		return fmt.Errorf("confpatch: write %s: %w", target, err)
	}
	return nil
}

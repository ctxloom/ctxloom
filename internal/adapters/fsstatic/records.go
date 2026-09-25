package fsstatic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	yamlv3 "gopkg.in/yaml.v3"

	// The formats a structured reversal is diffed in: registered HERE, by
	// the record that needs them, so a file's reversal never depends on
	// which engine happens to be linked into the binary.
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	_ "github.com/benjaminabbitt/hew/go/ext/toml"
	_ "github.com/benjaminabbitt/hew/go/ext/yaml"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// Records is the ONE ownership record (delivery.Ownership): per target file,
// the entries each writer owns in it and how to take that writer's
// contribution back out. A session's delivery and a materialize meet on one
// project file and each keeps its own tag; reconcile-to-empty removes only
// the calling writer's.
//
// The reversal is DIFFED from the before and after images through
// confpatch's hew machinery, as confpatch.Store's is:
// for a format hew reads (JSON, YAML, TOML) it is a hew patch, so the user's
// own entries in a shared file survive a writer's removal; for any other
// file the writer's contribution is the whole file, and the record keeps
// the bytes that stood there before it (none when ctxloom created it).
//
// Records are home-rooted, beside confpatch.Store's, for the same reason: the target
// is FOREIGN, so ctxloom never leaves its own state beside it. That is what
// replaced the ledger sidecar (.ctxloom-managed) every managed directory
// used to carry.
type Records struct {
	fs  afero.Fs
	dir string
}

var _ delivery.Ownership = (*Records)(nil)

// NewRecords opens the record store at dir on fs; dir is created on the
// first record written. An EXISTING dir is tightened to owner-only here as
// well as by Prepare; Prepare is the one a delivery relies on
// (delivery.Ownership.Prepare says why).
func NewRecords(recordFS afero.Fs, dir string) (*Records, error) {
	if recordFS == nil {
		return nil, errors.New("fsstatic: nil record filesystem")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("fsstatic: empty record directory")
	}
	r := &Records{fs: recordFS, dir: dir}
	if err := r.tighten(); err != nil {
		return nil, err
	}
	return r, nil
}

// Prepare tightens the record dir to owner-only on the real filesystem. The
// same directory (paths.HomeRecordsDir) holds the undo records engine
// approaches write through the delivery's copy-on-write overlay, which
// cannot chmod it (see confpatch.EnsureRecordDir).
func (r *Records) Prepare(context.Context) error { return r.tighten() }

// tighten brings an EXISTING record dir to owner-only; a missing one is left
// missing, and the first record written creates it owner-only.
func (r *Records) tighten() error {
	exists, err := afero.DirExists(r.fs, r.dir)
	if err != nil {
		return fmt.Errorf("fsstatic: stat %s: %w", r.dir, err)
	}
	if !exists {
		return nil
	}
	return confpatch.EnsureRecordDir(r.fs, r.dir)
}

// ownershipRecord is one target file's record on disk.
type ownershipRecord struct {
	Target  string `yaml:"target"`
	Created bool   `yaml:"created"` // ctxloom created the file: reconcile-to-empty by the last writer removes it
	// Writers is every writer's contribution, keyed by delivery.Writer.
	Writers map[string]writerRecord `yaml:"writers"`
}

// writerRecord is one writer's contribution to a target: the entries it
// owns, and the reversal — a hew patch for a structured format, else the
// bytes that stood there before this writer wrote (Before, with Existed
// saying whether the file was there at all).
type writerRecord struct {
	Entries   []string  `yaml:"entries"`
	AppliedAt time.Time `yaml:"applied_at"`
	Reversal  string    `yaml:"reversal,omitempty"`
	Existed   bool      `yaml:"existed"`
	Before    []byte    `yaml:"before,omitempty"`
}

const ownershipSuffix = ".ownership.yaml"

func (r *Records) path(target string) string {
	return filepath.Join(r.dir, confpatch.RecordPrefix(target)+ownershipSuffix)
}

// Apply reverses the writer's previous contribution to target, hands the
// restored file to build, writes what build returns and records the
// writer's entries with the reversal that takes them back out. A nil
// desired reconciles the writer to empty.
func (r *Records) Apply(_ context.Context, targetFS afero.Fs, target string, writer delivery.Writer, build delivery.Build) (delivery.Result, error) {
	var res delivery.Result
	if targetFS == nil || build == nil || strings.TrimSpace(target) == "" || writer == "" {
		return res, errors.New("fsstatic: Apply needs a target filesystem, a target, a writer and a build")
	}
	binding, format, structured := bindingFor(target)
	err := sessions.WithFileLock(targetFS, target, func() error {
		before, existed, err := confpatch.ReadTarget(targetFS, target)
		if err != nil {
			return err
		}
		rec, err := r.load(target)
		if err != nil {
			return err
		}
		restored, restoredExists, err := restore(binding, target, before, existed, rec.Writers[string(writer)], structured)
		if err != nil {
			return err
		}
		desired, entries, err := build(restored)
		if err != nil {
			return err
		}
		if desired == nil {
			return r.reconcile(targetFS, target, writer, rec, before, existed, restored, restoredExists, &res)
		}
		wr := writerRecord{Entries: slices.Clone(entries), AppliedAt: time.Now().UTC(), Existed: restoredExists}
		switch {
		case structured && (restoredExists || len(confpatch.EmptyDocument(format)) > 0):
			// A created file's reversal is diffed against the format's empty
			// document, so a reapply still takes the old entries out first.
			// A format with no empty document to diff from (YAML) leaves a
			// created file owned whole, like an opaque one.
			base := restored
			if !restoredExists || len(bytes.TrimSpace(base)) == 0 {
				base = confpatch.EmptyDocument(format)
			}
			reversal, err := provenReversal(binding, format, target, base, desired)
			if err != nil {
				return err
			}
			wr.Reversal = string(reversal)
		case restoredExists:
			wr.Before = slices.Clone(restored)
		}
		if !existed {
			rec.Created = true
		}
		if rec.Writers == nil {
			rec.Writers = map[string]writerRecord{}
		}
		rec.Writers[string(writer)] = wr
		if err := r.save(target, rec); err != nil {
			return err
		}
		if existed && bytes.Equal(before, desired) {
			return nil
		}
		res.Changed = true
		return confpatch.WriteTarget(targetFS, target, desired)
	})
	return res, err
}

// reconcile is the empty build: the writer's tag leaves the record and the
// file becomes the restored image. A file ctxloom created leaves with its
// last writer; while another writer's tag remains, a contribution that was
// the whole file leaves the file to that writer.
func (r *Records) reconcile(targetFS afero.Fs, target string, writer delivery.Writer, rec ownershipRecord, before []byte, existed bool, restored []byte, restoredExists bool, res *delivery.Result) error {
	if _, owned := rec.Writers[string(writer)]; !owned {
		return nil
	}
	delete(rec.Writers, string(writer))
	othersRemain := len(rec.Writers) > 0
	if othersRemain {
		if err := r.save(target, rec); err != nil {
			return err
		}
	} else if err := r.fs.Remove(r.path(target)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !existed {
		return nil
	}
	if !othersRemain && rec.Created || !restoredExists && !othersRemain {
		res.Changed = true
		return targetFS.Remove(target)
	}
	if !restoredExists || bytes.Equal(before, restored) {
		return nil
	}
	res.Changed = true
	return confpatch.WriteTarget(targetFS, target, restored)
}

// restore takes the writer's previous contribution back out of before: the
// recorded hew reversal for a structured file, the recorded pre-image for an
// opaque one. It reports whether a file stands after the restoration.
func restore(binding hew.Binding, target string, before []byte, existed bool, prev writerRecord, structured bool) ([]byte, bool, error) {
	if prev.AppliedAt.IsZero() || !existed {
		return before, existed, nil
	}
	if structured {
		if prev.Reversal == "" {
			// Nothing was diffed: a contribution identical to what stood
			// there, or a created file in a format with no empty document
			// (owned whole).
			if !prev.Existed {
				return nil, false, nil
			}
			return before, true, nil
		}
		// The reversed document STANDS even when this writer created the
		// file: another writer's entries may be in it, and a created file
		// with no writer left is the record's Created flag to remove.
		restored, err := confpatch.ApplyPatchText(binding, before, []byte(prev.Reversal), target)
		if err != nil {
			return nil, false, fmt.Errorf("fsstatic: %s has drifted since it was last written, so the previous contribution could not be reversed; refusing to write rather than clobber the change: %w", target, err)
		}
		return restored, true, nil
	}
	if !prev.Existed {
		return nil, false, nil
	}
	return prev.Before, true, nil
}

// provenReversal diffs after back to before and proves the patch applies to
// after. A byte-exact round trip is not demanded: a writer that re-renders
// the file in its own layout leaves the reversal semantically exact and
// byte-inexact, and the user's entries are what the reversal keeps.
func provenReversal(binding hew.Binding, format hew.FormatID, target string, before, after []byte) ([]byte, error) {
	if bytes.Equal(before, after) {
		return nil, nil
	}
	reversal, err := confpatch.RenderReversal(format, before, after, target)
	if err != nil {
		return nil, err
	}
	roundTripped, err := confpatch.ApplyPatchText(binding, after, reversal, target)
	if err != nil {
		return nil, fmt.Errorf("fsstatic: the reversal computed for %s does not apply to the document it was derived from; refusing to write: %w", target, err)
	}
	if !bytes.Equal(roundTripped, before) && !sameDocument(format, roundTripped, before, target) {
		return nil, fmt.Errorf("fsstatic: the reversal computed for %s applies but does not restore the document it was derived from; refusing to write an undo that does not undo", target)
	}
	return reversal, nil
}

// sameDocument reports whether two images are the same document under the
// format: parsed equality for JSON (a writer's re-rendering of the user's
// layout is not a change of content), an empty hew inverse otherwise.
func sameDocument(format hew.FormatID, a, b []byte, target string) bool {
	switch format {
	case hew.FormatJSON, hew.FormatJSONC:
		var da, db any
		if json.Unmarshal(a, &da) != nil || json.Unmarshal(b, &db) != nil {
			return false
		}
		return reflect.DeepEqual(da, db)
	}
	residue, err := hew.Invert(format, a, b, confpatch.InversionOptions(target))
	return err == nil && len(residue.Transform) == 0
}

// bindingFor names the hew binding a target's format has, when it has one.
func bindingFor(target string) (hew.Binding, hew.FormatID, bool) {
	format, ok := hew.DetectFormat(filepath.Base(target))
	if !ok {
		return hew.Binding{}, "", false
	}
	binding, ok := hew.Lookup(format)
	if !ok || binding.Applier == nil || binding.Document == nil {
		return hew.Binding{}, "", false
	}
	return binding, format, true
}

// Owned is one writer's entries in one target.
func (r *Records) Owned(target string, writer delivery.Writer) ([]string, error) {
	rec, err := r.load(target)
	if err != nil {
		return nil, err
	}
	return slices.Clone(rec.Writers[string(writer)].Entries), nil
}

// Targets lists the files a writer owns entries in, sorted.
func (r *Records) Targets(writer delivery.Writer) ([]string, error) {
	entries, err := afero.ReadDir(r.fs, r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ownershipSuffix) {
			continue
		}
		rec, err := r.read(filepath.Join(r.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		if _, ok := rec.Writers[string(writer)]; ok {
			out = append(out, rec.Target)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (r *Records) load(target string) (ownershipRecord, error) {
	rec, err := r.read(r.path(target))
	if os.IsNotExist(err) {
		return ownershipRecord{Target: target}, nil
	}
	return rec, err
}

func (r *Records) read(path string) (ownershipRecord, error) {
	data, err := afero.ReadFile(r.fs, path)
	if err != nil {
		return ownershipRecord{}, err
	}
	var rec ownershipRecord
	if err := yamlv3.Unmarshal(data, &rec); err != nil {
		return ownershipRecord{}, fmt.Errorf("fsstatic: read ownership record %s: %w", path, err)
	}
	return rec, nil
}

func (r *Records) save(target string, rec ownershipRecord) error {
	rec.Target = target
	data, err := yamlv3.Marshal(rec)
	if err != nil {
		return err
	}
	if err := confpatch.EnsureRecordDir(r.fs, r.dir); err != nil {
		return err
	}
	return iox.WriteFileAtomicFs(r.fs, r.path(target), data, 0o600)
}

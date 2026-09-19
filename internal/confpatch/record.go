package confpatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/benjaminabbitt/hew/go/hewfs"
	"github.com/spf13/afero"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// Record is hew's §9.7 application record, in Go structs marshaled straight
// through gopkg.in/yaml.v3: struct FIELD DECLARATION ORDER is what v3 emits
// (unlike a map, whose key order it would not preserve), so the field order
// below IS the on-disk key order.
type Record struct {
	Record    int            `yaml:"hew-record"`
	AppliedAt string         `yaml:"applied_at"`
	Patch     RecordPatch    `yaml:"patch"`
	Targets   []RecordTarget `yaml:"targets"`

	// Reversal is the rendered .hew patch that turns the after-image back into
	// the before-image — the artifact the next write applies to take ctxloom's
	// entries back out.
	//
	// It is stored as TEXT, not as a resolved op list, because text is what hew
	// can hand back to its own parser: rendering it here and re-parsing it in
	// applyPatchText proves the undo is usable at the moment it is written.
	//
	// The original reason was stronger — a resolved list could not be replayed
	// at all, because ResolvedOp dropped OnConflict and an `add` in it had lost
	// whether it meant replace, keep or fail. Upstream fixed that, and RecordOp
	// now carries the policy, so the resolved form is no longer lossy. Text
	// stays because it also sidesteps the inversion rules §9.7 explicitly
	// defers, which a replayed op list would still need.
	//
	// It is not a duplicate of Targets[].Inverse. That field is the §9.7
	// audit statement in the resolved form the spec requires; this is the
	// executable undo. Both describe the same change; only one can be applied.
	//
	// STRING, not []byte: gopkg.in/yaml.v3 emits a []byte as a sequence of
	// integers, one per line, so the patch arrived on disk as 266 lines of
	// decimal bytes — a record nobody can read defeats the point of writing an
	// auditable one. As a string it round-trips as the block of patch text it is.
	Reversal string `yaml:"reversal,omitempty"`
}

type RecordPatch struct {
	Source string `yaml:"source"`
	Digest string `yaml:"digest"`
}

type RecordTarget struct {
	Target     string     `yaml:"target"`
	Format     string     `yaml:"format"`
	Before     string     `yaml:"before"`
	After      string     `yaml:"after"`
	Committed  bool       `yaml:"committed"`
	Transforms []RecordOp `yaml:"transforms"`
	Inverse    []RecordOp `yaml:"inverse,omitempty"`
}

// RecordOp is one §9.2 RESOLVED op — the record's "transforms" field must hold
// the resolved list (indices concrete, key-matches collapsed), not the abstract
// patch, per §9.7: "the record states what happened to THIS file."
type RecordOp struct {
	Op         string `yaml:"op"`
	From       string `yaml:"from,omitempty"`
	Path       string `yaml:"path"`
	Absent     bool   `yaml:"absent,omitempty"`
	Count      *int   `yaml:"count,omitempty"`
	Kind       string `yaml:"kind,omitempty"`
	Exhaustive bool   `yaml:"exhaustive,omitempty"`

	// OnConflict is the add policy the transform carried. hew's OP-02/03/04 all
	// resolve to `add` and differ ONLY here — fail, replace, keep — so a record
	// that drops it says "something was added" without saying whether ctxloom
	// meant to overwrite a user's value, seed one, or refuse if present. That is
	// the difference between §9.7's audit statement and a note, and it is the
	// same distinction whose mishandling corrupted an array in hew's own
	// applier. hew.ResolvedOp carried no OnConflict until upstream added it.
	OnConflict string `yaml:"on_conflict,omitempty"`

	// Value is a VALUE, not a *Node, and that is load-bearing: gopkg.in/yaml.v3
	// cannot DECODE a scalar or a sequence into a *yaml.Node — only into a
	// yaml.Node. It decodes a mapping into either, which is what made the bug
	// look selective: `transforms` values are mappings and round-tripped, while
	// `inverse` values are scalars and sequences and did not, so every record
	// carrying one became unreadable to the Store.Last that has to re-read it.
	// omitempty still suppresses the field for a valueless op (remove).
	Value yamlv3.Node `yaml:"value,omitempty"`
}

// recordFileSuffix is the record's extension, named once because FreeRecordPath
// has to split a filename on it to insert its counter.
const recordFileSuffix = ".hew-record.yaml"

// Last returns the newest record ctxloom wrote for target.
//
// Newest is decided by the record's own applied_at, not by filename order:
// FreeRecordPath's collision counter ("-2") is appended AFTER the timestamp, so
// a purely lexical sort puts "-10" before "-2". That case is vanishingly rare
// (it needs two writes inside the same nanosecond) but sorting by the field
// that actually means "when" costs nothing and cannot be wrong.
func (s *Store) Last(target string) (Record, bool, error) {
	var newest Record
	var newestKey string
	found := false

	exists, err := afero.DirExists(s.fs, s.dir)
	if err != nil {
		return newest, false, fmt.Errorf("confpatch: stat %s: %w", s.dir, err)
	}
	if !exists {
		// No record directory yet means no prior application — the first write
		// to any target reaches here, so it is not an error.
		return newest, false, nil
	}
	// The directory exists, so it may hold records named under the earlier,
	// unbounded scheme. Rename them BEFORE matching by prefix, or a record
	// written by an older ctxloom is invisible and the next apply stacks a
	// second application on top of the first instead of reversing it.
	if err := s.renameLegacyRecords(); err != nil {
		return newest, false, err
	}
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		return newest, false, fmt.Errorf("confpatch: read %s: %w", s.dir, err)
	}
	prefix := recordPrefix(target)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasPrefix(n, prefix) && strings.HasSuffix(n, recordFileSuffix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	for _, n := range names {
		data, err := afero.ReadFile(s.fs, filepath.Join(s.dir, n))
		if err != nil {
			return newest, false, fmt.Errorf("confpatch: read record %s: %w", n, err)
		}
		var rec Record
		if err := yamlv3.Unmarshal(data, &rec); err != nil {
			return newest, false, fmt.Errorf("confpatch: parse record %s: %w", n, err)
		}
		// applied_at then filename: the filename tie-break keeps the choice
		// deterministic when two records share a timestamp.
		key := rec.AppliedAt + "\x00" + n
		if !found || key > newestKey {
			newest, newestKey, found = rec, key, true
		}
	}
	return newest, found, nil
}

// write builds and writes one §9.7 record.
func (s *Store) write(target string, format hew.FormatID, tl hew.TransformList, before, after, reversal []byte) (string, error) {
	binding, ok := hew.Lookup(format)
	if !ok || binding.Document == nil {
		return "", fmt.Errorf("confpatch: hew has no document reader for %q, so the applied transforms cannot be resolved into a §9.7 record", format)
	}
	// target is hew's diagnostics LABEL here, not a path it opens — Document
	// does no I/O. Passing the real path means a parse failure names the file
	// in hew's own error as well as in the wrap below.
	doc, err := binding.Document(target, before)
	if err != nil {
		return "", fmt.Errorf("confpatch: parse %s's pre-image to resolve the applied transforms: %w", target, err)
	}
	ops, err := hew.Resolve(tl, doc)
	if err != nil {
		return "", fmt.Errorf("confpatch: resolve the applied transforms against %s: %w", target, err)
	}

	inverse, err := inverseOps(binding, format, target, after, before)
	if err != nil {
		return "", err
	}

	at := time.Now().UTC()
	rec := Record{
		Record:    1,
		AppliedAt: at.Format(time.RFC3339Nano),
		Patch:     RecordPatch{Source: "-", Digest: hewfs.Digest(reversal)},
		Targets: []RecordTarget{{
			Target:     target,
			Format:     string(format),
			Before:     hewfs.Digest(before),
			After:      hewfs.Digest(after),
			Committed:  true,
			Transforms: ResolvedOpsToRecord(ops),
			Inverse:    ResolvedOpsToRecord(inverse),
		}},
		Reversal: string(reversal),
	}

	out, err := yamlv3.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("confpatch: marshal application record: %w", err)
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("confpatch: create %s: %w", s.dir, err)
	}
	recordPath, err := FreeRecordPath(s.fs, s.dir, target, at)
	if err != nil {
		return "", err
	}
	if err := agent.AtomicWriteFile(s.fs, recordPath, out, filepath.Base(recordPath)); err != nil {
		return "", fmt.Errorf("confpatch: write %s: %w", recordPath, err)
	}
	// The record just written SUPERSEDES every earlier one for this target, so
	// the earlier ones go. Deliberately after the write, never before: the
	// window where a target has no record at all is the one state this store
	// must never be in.
	//
	// A prune failure does not fail the apply. The file is written and the undo
	// is durable at this point; leaving a stale sibling behind costs a few
	// kilobytes, while returning an error here would report a completed,
	// recorded application as failed and invite the caller to retry it.
	_ = s.pruneSuperseded(target, recordPath)
	return recordPath, nil
}

// pruneSuperseded removes every record for target except keep.
//
// ONLY THE NEWEST RECORD IS LIVE, and that is a property of how Apply works
// rather than a retention policy chosen here. Each apply REVERSES the previous
// application before computing the next, so the reversal it stores runs from
// the file as just written back to the user's own content — with every ctxloom
// entry already out of it. That single reversal is a complete undo; the ones
// before it describe a document that no longer exists on disk and undo nothing
// reachable.
//
// Left unpruned they simply accumulated: one file per apply per target,
// forever, in a home-rooted directory nothing ever swept. `Last` reads the
// whole directory on EVERY apply to find the newest, so the pile also made the
// operation it served steadily slower.
//
// The §9.7 AUDIT TRAIL is the cost, and it is stated rather than hidden: this
// keeps what can still be applied, not a history of what once was. A store that
// must retain the history needs a retention policy and somewhere to put it,
// which is a different piece of work from not leaking files.
func (s *Store) pruneSuperseded(target, keep string) error {
	prefix := recordPrefix(target)
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		return err
	}
	keepBase := filepath.Base(keep)
	var errs []error
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == keepBase {
			continue
		}
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, recordFileSuffix) {
			continue
		}
		if err := s.fs.Remove(filepath.Join(s.dir, name)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// inverseOps is the resolved op list that turns after back into before — the
// §9.7 audit statement. hew.Invert owns the derivation, including the
// direction: Diff(before, after) and Diff(after, before) are both well-formed
// and only one undoes anything, so hew decides it once rather than every
// consumer deciding it again.
//
// Resolved against the AFTER image because that is the document an undo would
// be applied to: the pointers have to name positions in the file as it stands
// now, not as it stood before the write.
func inverseOps(b hew.Binding, format hew.FormatID, target string, after, before []byte) ([]hew.ResolvedOp, error) {
	tl, err := hew.Invert(format, before, after, inversionOptions(target))
	if err != nil {
		return nil, fmt.Errorf("confpatch: derive the inverse of the application to %s: %w", target, err)
	}
	doc, err := b.Document(target, after)
	if err != nil {
		return nil, fmt.Errorf("confpatch: parse %s's post-image to resolve the inverse: %w", target, err)
	}
	return hew.Resolve(tl, doc)
}

// FreeRecordPath is the record path under dir that does not already exist,
// disambiguating with a counter when it does.
//
// AtomicWriteFile OVERWRITES, so the timestamp alone was never a defence: two
// applies against the same target in the same instant would produce the same
// name and the second would silently destroy the first — the exact evidence the
// record exists to preserve, gone on a success path. This loop makes that
// impossible, which is the difference between an invariant and a hope.
//
// It disambiguates rather than refusing because refusing also loses a record:
// the point is that no apply goes unrecorded, not that a particular filename
// is available.
//
// Exported because cli's config-write path writes the same record shape into
// the same directory and must name its files the same way; two copies of the
// naming are how one of them ends up unbounded again.
func FreeRecordPath(fs afero.Fs, dir, target string, at time.Time) (string, error) {
	base := RecordFilename(target, at)
	path := filepath.Join(dir, base)
	for n := 2; ; n++ {
		exists, err := afero.Exists(fs, path)
		if err != nil {
			return "", fmt.Errorf("confpatch: check %s: %w", path, err)
		}
		if !exists {
			return path, nil
		}
		path = filepath.Join(dir, strings.TrimSuffix(base, recordFileSuffix)+fmt.Sprintf("-%d", n)+recordFileSuffix)
	}
}

// RecordFilename names target's record: its bounded flat name (see
// paths.FlatName), then a sortable UTC timestamp, then the record suffix. A
// record is an audit trail entry, not a mutable sidecar: two applies against
// the same target must not overwrite each other's record.
//
// NANOSECONDS, not seconds. The clock is a parameter so the collision case is
// reachable from a test — with time.Now() inlined here, two applies could only
// be made to collide by running them inside the same second, which is a race a
// test cannot state.
func RecordFilename(target string, at time.Time) string {
	return recordPrefix(target) + at.UTC().Format(recordStampLayout) + recordFileSuffix
}

// recordStampLayout is the timestamp part of a record's name.
const recordStampLayout = "20060102T150405.000000000Z"

// recordPrefix is the part of a record's filename that identifies its target:
// what Last and pruneSuperseded match on, and what RecordFilename builds on.
func recordPrefix(target string) string {
	return paths.FlatName(target) + "__"
}

// legacyRecordPrefix is the prefix records carried before the name was
// bounded: the target's WHOLE path, flattened. It exists only so
// renameLegacyRecords can recognise a record written under that scheme;
// nothing writes it.
func legacyRecordPrefix(target string) string {
	return strings.ReplaceAll(filepath.ToSlash(target), "/", "__") + "__"
}

// renameLegacyRecords moves every record under s.dir (and any store nested
// below it — taskloom keeps its own in a subdirectory) from the unbounded
// name to the bounded one. The record body carries the target's full path,
// so the new name is computed from the file itself; the timestamp and any
// collision counter after the target part are kept as they are.
//
// IDEMPOTENT, because every launch may run it: a record already carrying the
// bounded prefix for its own target is left alone, and a rename whose
// destination already exists is skipped rather than overwriting — losing a
// record is the one thing a rename must never do. A file whose body names no
// target, or whose name matches neither scheme, is not this migration's to
// touch and stays where it is.
//
// Candidates are collected first and renamed after the walk, so the walk
// never sees a directory changing under it.
func (s *Store) renameLegacyRecords() error {
	type move struct{ from, to string }
	var moves []move
	walkErr := afero.Walk(s.fs, s.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(info.Name(), recordFileSuffix) {
			return nil
		}
		target, ok := recordTarget(s.fs, path)
		if !ok {
			return nil
		}
		name := info.Name()
		if strings.HasPrefix(name, recordPrefix(target)) {
			return nil
		}
		legacy := legacyRecordPrefix(target)
		if !strings.HasPrefix(name, legacy) {
			return nil
		}
		to := filepath.Join(filepath.Dir(path), recordPrefix(target)+strings.TrimPrefix(name, legacy))
		moves = append(moves, move{from: path, to: to})
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("confpatch: scan %s for records to rename: %w", s.dir, walkErr)
	}
	var errs []error
	for _, m := range moves {
		exists, err := afero.Exists(s.fs, m.to)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if exists {
			continue
		}
		if err := s.fs.Rename(m.from, m.to); err != nil {
			errs = append(errs, fmt.Errorf("confpatch: rename record %s: %w", m.from, err))
		}
	}
	return errors.Join(errs...)
}

// recordTarget reads the target a record on disk describes. Only the one
// field is decoded: the rename needs the path and nothing else, and decoding
// less is what lets a record this version cannot otherwise parse still be
// renamed rather than stranded.
func recordTarget(fs afero.Fs, path string) (string, bool) {
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		return "", false
	}
	var rec struct {
		Targets []struct {
			Target string `yaml:"target"`
		} `yaml:"targets"`
	}
	if err := yamlv3.Unmarshal(data, &rec); err != nil || len(rec.Targets) == 0 || rec.Targets[0].Target == "" {
		return "", false
	}
	return rec.Targets[0].Target, true
}

// ResolvedOpsToRecord adapts hew.ResolvedOp (the library's form) to RecordOp
// (this package's yaml-tagged mirror of it): hew.ResolvedOp carries no yaml
// tags of its own, being built for hewcli's hand-rolled node marshaling rather
// than gopkg.in/yaml.v3's struct path.
func ResolvedOpsToRecord(ops []hew.ResolvedOp) []RecordOp {
	out := make([]RecordOp, len(ops))
	for i, op := range ops {
		out[i] = RecordOp{
			Op:         string(op.Op),
			From:       op.From,
			Path:       op.Path,
			Absent:     op.Absent,
			Count:      op.Count,
			Exhaustive: op.Exhaustive,
			OnConflict: string(op.OnConflict),
		}
		if op.NodeKind != nil {
			out[i].Kind = string(*op.NodeKind)
		}
		if !op.Value.IsZero() {
			if n := op.Value.Node(); n != nil {
				out[i].Value = *n
			}
		}
	}
	return out
}

package confpatch

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	// can hand back to its own parser. A resolved list cannot be replayed
	// faithfully: ResolvedOp carries no OnConflict, so an `add` in it has lost
	// whether it meant replace, keep or fail (hew task alienable-flatterer),
	// and reconstructing that needs the inversion rules §9.7 explicitly defers.
	// Text sidesteps the question entirely.
	//
	// It is not a duplicate of Targets[].Inverse. That field is the §9.7
	// audit statement in the resolved form the spec requires; this is the
	// executable undo. Both describe the same change; only one can be applied.
	Reversal []byte `yaml:"reversal,omitempty"`
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
	Op         string       `yaml:"op"`
	From       string       `yaml:"from,omitempty"`
	Path       string       `yaml:"path"`
	Absent     bool         `yaml:"absent,omitempty"`
	Count      *int         `yaml:"count,omitempty"`
	Kind       string       `yaml:"kind,omitempty"`
	Exhaustive bool         `yaml:"exhaustive,omitempty"`
	Value      *yamlv3.Node `yaml:"value,omitempty"`
}

// recordFileSuffix is the record's extension, named once because freeRecordPath
// has to split a filename on it to insert its counter.
const recordFileSuffix = ".hew-record.yaml"

// Last returns the newest record ctxloom wrote for target.
//
// Newest is decided by the record's own applied_at, not by filename order:
// freeRecordPath's collision counter ("-2") is appended AFTER the timestamp, so
// a purely lexical sort puts "-10" before "-2". That case is vanishingly rare
// (it needs two writes inside the same nanosecond) but sorting by the field
// that actually means "when" costs nothing and cannot be wrong.
func (s *Store) Last(target string) (Record, bool, error) {
	var newest Record
	var newestKey string
	found := false

	prefix := flattenTarget(target) + "__"
	entries, err := afero.ReadDir(s.fs, s.dir)
	if err != nil {
		// No record directory yet means no prior application — the first write
		// to any target reaches here, so it is not an error.
		return newest, false, nil
	}
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
		Patch:     RecordPatch{Source: "-", Digest: sha256Digest(reversal)},
		Targets: []RecordTarget{{
			Target:     target,
			Format:     string(format),
			Before:     sha256Digest(before),
			After:      sha256Digest(after),
			Committed:  true,
			Transforms: resolvedOpsToRecord(ops),
			Inverse:    resolvedOpsToRecord(inverse),
		}},
		Reversal: reversal,
	}

	out, err := yamlv3.Marshal(rec)
	if err != nil {
		return "", fmt.Errorf("confpatch: marshal application record: %w", err)
	}
	if err := s.fs.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("confpatch: create %s: %w", s.dir, err)
	}
	recordPath, err := s.freePath(target, at)
	if err != nil {
		return "", err
	}
	if err := agent.AtomicWriteFile(s.fs, recordPath, out, filepath.Base(recordPath)); err != nil {
		return "", fmt.Errorf("confpatch: write %s: %w", recordPath, err)
	}
	return recordPath, nil
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
	tl, err := hew.Invert(format, before, after, hew.DiffOptions{Target: target})
	if err != nil {
		return nil, fmt.Errorf("confpatch: derive the inverse of the application to %s: %w", target, err)
	}
	doc, err := b.Document(target, after)
	if err != nil {
		return nil, fmt.Errorf("confpatch: parse %s's post-image to resolve the inverse: %w", target, err)
	}
	return hew.Resolve(tl, doc)
}

// freePath is the record path that does not already exist, disambiguating with
// a counter when it does.
//
// AtomicWriteFile OVERWRITES, so the timestamp alone was never a defence: two
// applies against the same target in the same instant would produce the same
// name and the second would silently destroy the first — the exact evidence the
// record exists to preserve, gone on a success path. This loop makes that
// impossible, which is the difference between an invariant and a hope.
func (s *Store) freePath(target string, at time.Time) (string, error) {
	base := recordFilename(target, at)
	path := filepath.Join(s.dir, base)
	for n := 2; ; n++ {
		exists, err := afero.Exists(s.fs, path)
		if err != nil {
			return "", fmt.Errorf("confpatch: check %s: %w", path, err)
		}
		if !exists {
			return path, nil
		}
		path = filepath.Join(s.dir, strings.TrimSuffix(base, recordFileSuffix)+fmt.Sprintf("-%d", n)+recordFileSuffix)
	}
}

// recordFilename flattens target into one filename component and suffixes a
// sortable UTC timestamp, because a record is an audit trail entry, not a
// mutable sidecar: two applies against the same target must not overwrite each
// other's record.
//
// NANOSECONDS, not seconds. The clock is a parameter so the collision case is
// reachable from a test — with time.Now() inlined here, two applies could only
// be made to collide by running them inside the same second, which is a race a
// test cannot state.
func recordFilename(target string, at time.Time) string {
	return flattenTarget(target) + "__" + at.UTC().Format("20060102T150405.000000000Z") + recordFileSuffix
}

// flattenTarget turns a path into one filename component, the same way
// filelock's HomePathFor flattens a protected path: forward-slash it, then
// "/" -> "__".
func flattenTarget(target string) string {
	return strings.ReplaceAll(filepath.ToSlash(target), "/", "__")
}

// resolvedOpsToRecord adapts hew.ResolvedOp (the library's form) to RecordOp
// (this package's yaml-tagged mirror of it): hew.ResolvedOp carries no yaml
// tags of its own, being built for hewcli's hand-rolled node marshaling rather
// than gopkg.in/yaml.v3's struct path.
func resolvedOpsToRecord(ops []hew.ResolvedOp) []RecordOp {
	out := make([]RecordOp, len(ops))
	for i, op := range ops {
		out[i] = RecordOp{
			Op:         string(op.Op),
			From:       op.From,
			Path:       op.Path,
			Absent:     op.Absent,
			Count:      op.Count,
			Exhaustive: op.Exhaustive,
		}
		if op.NodeKind != nil {
			out[i].Kind = string(*op.NodeKind)
		}
		if !op.Value.IsZero() {
			out[i].Value = op.Value.Node()
		}
	}
	return out
}

func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

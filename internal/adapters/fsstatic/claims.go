package fsstatic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	yamlv3 "gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// Claims is the ownership record: one record per TARGET FILE, naming for each
// place in the file the writers that put a value there and the values they
// put. A writer's release takes out only what no other writer still claims,
// so two writers sharing an entry, or one creating the container another
// writes under, never take each other's out.
//
// The record holds ctxloom's own values and nothing else. A place is claimed
// only where it is absent, already holds the claimed value, or holds an entry
// that runs ctxloom (confpatch.OwnedBy); a user's value there is refused
// (ErrNotOurs), never displaced, so no user value — and no user secret — is
// ever copied into the record.
//
// Records live under the home, never beside the target, because the target is
// FOREIGN.
type Claims struct {
	fs  afero.Fs
	dir string
}

// Claim is one value a writer puts at one place in a target file. Pointer is
// an RFC 6901 pointer into a file hew reads, and Value anything hew encodes;
// the empty Pointer claims the whole file, and Value is then its bytes. Via
// names what the claim came through (a companion), for a release to keep.
type Claim struct {
	Pointer string
	Via     string
	Value   any
}

// PathState is one claimed place: its writers, the one whose value the file
// holds first, and whether the file holds that value now.
type PathState struct {
	Pointer string
	Writers []delivery.Writer
	Live    bool
}

// ErrNotOurs refuses a write over a value that is not ctxloom's: a user's
// value at a place a claim names, or a claimed value the user has since
// changed.
var ErrNotOurs = errors.New("the value there is not ctxloom's")

// NotOursError is ErrNotOurs naming the place.
type NotOursError struct{ Target, Pointer string }

func (e *NotOursError) Error() string {
	where := e.Pointer
	if where == "" {
		where = "the whole file"
	}
	return fmt.Sprintf("fsstatic: %s: %s: %v; refusing to change it", e.Target, where, ErrNotOurs)
}

// Unwrap makes errors.Is(err, ErrNotOurs) true.
func (e *NotOursError) Unwrap() error { return ErrNotOurs }

const (
	claimsSuffix  = ".claims.yaml"
	claimsVersion = 2
	// ctxloomOwner is the executable basename that proves an unclaimed entry
	// is ctxloom's own (confpatch.OwnedBy).
	ctxloomOwner = "ctxloom"
)

// NewClaims opens the record store at dir on fs. dir is created on the first
// record written; an existing one is tightened to owner-only.
func NewClaims(recordFS afero.Fs, dir string) (*Claims, error) {
	if recordFS == nil {
		return nil, errors.New("fsstatic: nil record filesystem")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("fsstatic: empty record directory")
	}
	c := &Claims{fs: recordFS, dir: dir}
	return c, c.Prepare(context.Background())
}

// Prepare tightens an EXISTING record dir to owner-only (delivery.Ownership's
// security invariant); a missing one is left missing.
func (c *Claims) Prepare(context.Context) error {
	exists, err := afero.DirExists(c.fs, c.dir)
	if err != nil {
		return fmt.Errorf("fsstatic: stat %s: %w", c.dir, err)
	}
	if !exists {
		return nil
	}
	return confpatch.EnsureRecordDir(c.fs, c.dir)
}

// claimsRecord is one target's record on disk.
type claimsRecord struct {
	Version    int                     `yaml:"claims"`
	Target     string                  `yaml:"target"`
	Created    bool                    `yaml:"created"`
	Containers []string                `yaml:"containers,omitempty"`
	Found      []string                `yaml:"found,omitempty"`
	Seq        uint64                  `yaml:"seq"`
	Paths      map[string][]claimEntry `yaml:"paths"`
	Pending    *pendingWrite           `yaml:"pending,omitempty"`
}

// claimEntry is one writer's claim at one place. Seq orders claims of one
// writer kind: the latest staged is on top.
type claimEntry struct {
	Writer string `yaml:"writer"`
	Via    string `yaml:"via,omitempty"`
	Seq    uint64 `yaml:"seq"`
	Value  any    `yaml:"value"`
	Bytes  []byte `yaml:"bytes,omitempty"`
}

// pendingWrite is written WITH the record, before the target: the target's
// digest before and after the write, and the state the file was in before
// it. The next load tells from the target's digest whether the write landed.
type pendingWrite struct {
	Before string    `yaml:"before"`
	After  string    `yaml:"after"`
	Prior  fileState `yaml:"prior"`
}

// fileState is what ctxloom holds in a file: the effective claim at each
// place, the containers it created, the places whose value it FOUND there
// already (which the last release leaves) and whether it created the file.
type fileState struct {
	Created    bool                  `yaml:"created"`
	Containers []string              `yaml:"containers,omitempty"`
	Found      []string              `yaml:"found,omitempty"`
	Values     map[string]claimEntry `yaml:"values,omitempty"`
}

func (c *Claims) path(target string) string {
	return filepath.Join(c.dir, confpatch.RecordPrefix(target)+claimsSuffix)
}

func (c *Claims) load(target string) (claimsRecord, []byte, error) {
	data, err := afero.ReadFile(c.fs, c.path(target))
	if os.IsNotExist(err) {
		return claimsRecord{Version: claimsVersion, Target: target}, nil, nil
	}
	if err != nil {
		return claimsRecord{}, nil, err
	}
	var rec claimsRecord
	if err := yamlv3.Unmarshal(data, &rec); err != nil {
		return claimsRecord{}, nil, fmt.Errorf("fsstatic: read claims record %s: %w", c.path(target), err)
	}
	if rec.Version != claimsVersion {
		return claimsRecord{}, nil, fmt.Errorf("fsstatic: claims record %s is version %d; this ctxloom reads version %d", c.path(target), rec.Version, claimsVersion)
	}
	return rec, data, nil
}

// rank orders writer kinds: a session's value is effective over the
// project's at the same place, whichever staged last (SESSION OVER PROJECT).
func rank(w string) int {
	if _, ok := delivery.Writer(w).SessionHarp(); ok {
		return 1
	}
	return 0
}

// ordered is entries effective first: by writer kind, then latest staged.
func ordered(entries []claimEntry) []claimEntry {
	out := slices.Clone(entries)
	sort.SliceStable(out, func(i, j int) bool {
		if ri, rj := rank(out[i].Writer), rank(out[j].Writer); ri != rj {
			return ri > rj
		}
		return out[i].Seq > out[j].Seq
	})
	return out
}

func (rec *claimsRecord) clone() claimsRecord {
	out := *rec
	out.Paths = make(map[string][]claimEntry, len(rec.Paths))
	for p, entries := range rec.Paths {
		out.Paths[p] = slices.Clone(entries)
	}
	return out
}

func (rec *claimsRecord) state() fileState {
	s := fileState{Created: rec.Created, Containers: slices.Clone(rec.Containers), Found: slices.Clone(rec.Found), Values: map[string]claimEntry{}}
	for p, entries := range rec.Paths {
		if len(entries) > 0 {
			s.Values[p] = ordered(entries)[0]
		}
	}
	return s
}

func (rec *claimsRecord) stage(w delivery.Writer, claims []Claim) {
	rec.Seq++
	if rec.Paths == nil {
		rec.Paths = map[string][]claimEntry{}
	}
	for _, cl := range claims {
		e := claimEntry{Writer: string(w), Via: cl.Via, Seq: rec.Seq}
		if cl.Pointer == "" {
			e.Bytes = cl.Value.([]byte)
		} else {
			e.Value = cl.Value
		}
		rec.Paths[cl.Pointer] = append(dropWriter(rec.Paths[cl.Pointer], string(w), nil, cl.Pointer), e)
	}
}

// settle gives every claim that was already in was, unchanged, its place
// back: a redelivery restating a claim is not a newer claim, and a record
// that changed on every redelivery would be written on every redelivery.
func (rec *claimsRecord) settle(was claimsRecord) {
	changed := false
	for p, entries := range rec.Paths {
		for i, e := range entries {
			j := slices.IndexFunc(was.Paths[p], func(x claimEntry) bool { return x.Writer == e.Writer && sameClaim(x, e) })
			if j < 0 {
				changed = true
				continue
			}
			entries[i].Seq = was.Paths[p][j].Seq
		}
	}
	if !changed && len(rec.Paths) == len(was.Paths) {
		rec.Seq = was.Seq
	}
}

func sameClaim(a, b claimEntry) bool {
	return a.Via == b.Via && bytes.Equal(a.Bytes, b.Bytes) && reflect.DeepEqual(a.Value, b.Value)
}

func (rec *claimsRecord) release(w delivery.Writer, keep func(pointer, via string) bool) {
	for p, entries := range rec.Paths {
		if rest := dropWriter(entries, string(w), keep, p); len(rest) > 0 {
			rec.Paths[p] = rest
		} else {
			delete(rec.Paths, p)
		}
	}
}

func dropWriter(entries []claimEntry, w string, keep func(pointer, via string) bool, pointer string) []claimEntry {
	var out []claimEntry
	for _, e := range entries {
		if e.Writer != w || (keep != nil && keep(pointer, e.Via)) {
			out = append(out, e)
		}
	}
	return out
}

// Staging stages claim changes into one Batch: every Stage and Release on a
// target folds into that target's ONE write, and its record is written by the
// target's seal, under the target's lock, before the target.
type Staging struct {
	c       *Claims
	b       *safefs.Batch
	targets map[string]*targetOps
}

// In is a staging bound to b.
func (c *Claims) In(b *safefs.Batch) *Staging {
	return &Staging{c: c, b: b, targets: map[string]*targetOps{}}
}

type claimOp struct {
	writer delivery.Writer
	claims []Claim // nil for a release
	keep   func(pointer, via string) bool
	stage  bool
}

type targetOps struct {
	c      *Claims
	target string
	ops    []claimOp
	// set by the fold for the seal
	rec   claimsRecord
	disk  []byte
	prior fileState
}

func (s *Staging) touch(target string) *targetOps {
	t, ok := s.targets[target]
	if !ok {
		t = &targetOps{c: s.c, target: target}
		s.targets[target] = t
		s.b.Edit(target, t.fold)
		s.b.Seal(target, t.seal)
	}
	return t
}

// Stage puts w's claims into target, each replacing w's earlier claim at the
// same place. A place w claimed before and does not name here keeps its
// claim; Release is what drops claims.
func (s *Staging) Stage(target string, w delivery.Writer, claims []Claim) error {
	if w == "" || strings.TrimSpace(target) == "" {
		return errors.New("fsstatic: a stage needs a target and a writer")
	}
	seen := map[string]bool{}
	for i, cl := range claims {
		if seen[cl.Pointer] {
			return fmt.Errorf("fsstatic: %s: %s is claimed twice in one stage", target, cl.Pointer)
		}
		seen[cl.Pointer] = true
		if cl.Pointer == "" {
			if _, ok := cl.Value.([]byte); !ok {
				return fmt.Errorf("fsstatic: %s: a whole-file claim's value is its bytes", target)
			}
			continue
		}
		if _, _, ok := bindingFor(target); !ok {
			return fmt.Errorf("fsstatic: %s: claims %s, but no format hew reads names this file", target, cl.Pointer)
		}
		if _, err := pointerPath(cl.Pointer); err != nil {
			return fmt.Errorf("fsstatic: %s: %w", target, err)
		}
		v, err := canon(cl.Value)
		if err != nil {
			return fmt.Errorf("fsstatic: %s: the value claimed at %s: %w", target, cl.Pointer, err)
		}
		claims[i].Value = v
	}
	if seen[""] && len(claims) > 1 {
		return fmt.Errorf("fsstatic: %s: a whole-file claim cannot share a stage with a claim inside the file", target)
	}
	t := s.touch(target)
	t.ops = append(t.ops, claimOp{writer: w, claims: slices.Clone(claims), stage: true})
	return nil
}

// Release drops w's claims in target, except those keep names (keep may be
// nil). What no writer still claims leaves the file; a place another writer
// claims takes that writer's value.
func (s *Staging) Release(target string, w delivery.Writer, keep func(pointer, via string) bool) error {
	if w == "" || strings.TrimSpace(target) == "" {
		return errors.New("fsstatic: a release needs a target and a writer")
	}
	t := s.touch(target)
	t.ops = append(t.ops, claimOp{writer: w, keep: keep})
	return nil
}

// fold loads the record under the target's lock, completes a write a crash
// left behind, applies every staged op to the record and moves the file from
// the state the record described to the state it now describes: one
// transition, so the file changes once however many ops it took.
func (t *targetOps) fold(cur []byte, exists bool) ([]byte, bool, error) {
	rec, disk, err := t.c.load(t.target)
	if err != nil {
		return nil, false, err
	}
	t.disk, t.prior = disk, rec.state()
	// A pending note whose write landed is left as it is: it is still true,
	// and clearing it would be a record write that changes nothing.
	if p := rec.Pending; p != nil && digest(cur, exists) != p.After {
		rec.Pending = nil
		if digest(cur, exists) == p.Before {
			// The record landed and the target did not: redo the write the
			// record describes, from the state the file is still in.
			t.prior = p.Prior
			if cur, exists, err = t.transition(cur, exists, p.Prior, &rec); err != nil {
				return nil, false, err
			}
		}
	}
	from, was := rec.state(), rec.clone()
	for _, op := range t.ops {
		if op.stage {
			rec.stage(op.writer, op.claims)
		} else {
			rec.release(op.writer, op.keep)
		}
	}
	rec.settle(was)
	next, keep, err := t.transition(cur, exists, from, &rec)
	t.rec = rec
	return next, keep, err
}

// seal writes the record before the target: with a pending note when the
// target is about to change, and not at all when nothing in it changed. A
// record left with no claims is removed once its target is settled — kept,
// with its note, while the write that emptied it may not have landed. The
// record this one replaced is deleted by its exact name.
func (t *targetOps) seal(before []byte, existed bool, after []byte, keep bool) error {
	rec := t.rec
	changing := digest(before, existed) != digest(after, keep)
	if changing {
		rec.Pending = &pendingWrite{Before: digest(before, existed), After: digest(after, keep), Prior: t.prior}
	}
	if err := t.c.dropOldRecord(t.target); err != nil {
		return err
	}
	path := t.c.path(t.target)
	if len(rec.Paths) == 0 && !changing {
		if err := t.c.fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	rec.Version, rec.Target = claimsVersion, t.target
	data, err := yamlv3.Marshal(rec)
	if err != nil {
		return err
	}
	if bytes.Equal(data, t.disk) {
		return nil
	}
	if err := confpatch.EnsureRecordDir(t.c.fs, t.c.dir); err != nil {
		return err
	}
	return safefs.WriteFile(t.c.fs, path, data, owneronly.FileMode, safefs.Durable())
}

// dropOldRecord deletes the per-writer ownership record this record replaces,
// recognized by its exact name.
func (c *Claims) dropOldRecord(target string) error {
	old := filepath.Join(c.dir, confpatch.RecordPrefix(target)+ownershipSuffix)
	if err := c.fs.Remove(old); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstatic: remove the superseded record %s: %w", old, err)
	}
	return nil
}

// digest names a target's content, or its absence, for the pending note.
func digest(data []byte, exists bool) string {
	if !exists {
		return "absent"
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// transition moves cur from the state from describes to the state rec now
// describes, recording in rec the containers and the file it creates.
func (t *targetOps) transition(cur []byte, exists bool, from fileState, rec *claimsRecord) ([]byte, bool, error) {
	to := rec.state()
	if _, whole := to.Values[""]; whole {
		return t.wholeFile(cur, exists, from, to, rec)
	}
	if _, whole := from.Values[""]; whole {
		return t.wholeFile(cur, exists, from, to, rec)
	}
	if len(from.Values) == 0 && len(to.Values) == 0 && len(to.Containers) == 0 {
		return cur, exists, nil
	}
	return t.structured(cur, exists, from, to, rec)
}

// wholeFile is the transition of a file claimed whole.
func (t *targetOps) wholeFile(cur []byte, exists bool, from, to fileState, rec *claimsRecord) ([]byte, bool, error) {
	o, hasO := from.Values[""]
	n, hasN := to.Values[""]
	if exists {
		ours := (hasO && bytes.Equal(cur, o.Bytes)) || (hasN && bytes.Equal(cur, n.Bytes))
		if !ours {
			if hasO && hasN && bytes.Equal(o.Bytes, n.Bytes) {
				return cur, exists, nil // an unchanged claim the user has since edited: left alone
			}
			return nil, false, &NotOursError{Target: t.target}
		}
	}
	if hasN {
		if !exists {
			rec.Created = true
		}
		return n.Bytes, true, nil
	}
	if !exists || !rec.Created {
		return cur, exists, nil
	}
	rec.Created = false
	return nil, false, nil
}

// structured is the transition of a file hew reads, place by place: only the
// places whose effective value changed are checked and touched, plus a
// claimed place the file has lost, which is put back.
func (t *targetOps) structured(cur []byte, exists bool, from, to fileState, rec *claimsRecord) ([]byte, bool, error) {
	binding, format, ok := bindingFor(t.target)
	if !ok {
		return nil, false, fmt.Errorf("fsstatic: %s: no format hew reads names this file", t.target)
	}
	doc := cur
	if !exists {
		doc = confpatch.EmptyDocument(format)
	}
	e := &editor{target: t.target, binding: binding, format: format, doc: doc}
	pointers := unionKeys(from.Values, to.Values)
	// Removals deepest first, so a member leaves before its container is
	// judged. A place whose value ctxloom found there is left as found.
	for i := len(pointers) - 1; i >= 0; i-- {
		p := pointers[i]
		if _, hasN := to.Values[p]; hasN {
			continue
		}
		if slices.Contains(rec.Found, p) {
			rec.Found = slices.DeleteFunc(rec.Found, func(f string) bool { return f == p })
			continue
		}
		if err := e.remove(p, from.Values[p].Value); err != nil {
			return nil, false, err
		}
	}
	for _, p := range pointers {
		n, hasN := to.Values[p]
		if !hasN {
			continue
		}
		o, hasO := from.Values[p]
		created, found, err := e.set(p, o.Value, hasO, n.Value)
		if err != nil {
			return nil, false, err
		}
		rec.Containers = appendNew(rec.Containers, created...)
		if found {
			rec.Found = appendNew(rec.Found, p)
		}
	}
	if err := e.err(); err != nil {
		return nil, false, err
	}
	rec.Containers = e.prune(rec.Containers)
	if !exists && !bytes.Equal(e.doc, doc) {
		rec.Created = true
	}
	if rec.Created && len(to.Values) == 0 && bytes.Equal(bytes.TrimSpace(e.doc), bytes.TrimSpace(confpatch.EmptyDocument(format))) {
		rec.Created = false
		return nil, false, nil
	}
	if !exists && bytes.Equal(e.doc, doc) {
		return cur, exists, nil
	}
	return e.doc, true, nil
}

// editor applies one place's change at a time to doc through hew, reading the
// document again for the next: each change is judged against the document
// the previous one produced.
type editor struct {
	target  string
	binding hew.Binding
	format  hew.FormatID
	doc     []byte
	failed  error
}

func (e *editor) err() error { return e.failed }

func (e *editor) node(pointer string) (hew.Node, bool, error) {
	d, err := e.binding.Document(e.target, e.doc)
	if err != nil {
		return nil, false, fmt.Errorf("fsstatic: %s does not parse: %w", e.target, err)
	}
	n, ok := confpatch.NodeAt(d.Root(), pointer)
	return n, ok, nil
}

func (e *editor) apply(pointer string, op func(*hew.Sel)) error {
	p, err := pointerPath(pointer)
	if err != nil {
		return err
	}
	d, err := hew.OpenBytes(e.target, e.doc, hew.As(e.format))
	if err != nil {
		return fmt.Errorf("fsstatic: %s does not parse: %w", e.target, err)
	}
	op(d.AtPath(p))
	out, err := d.Bytes()
	if err != nil {
		return fmt.Errorf("fsstatic: %s: %s: %w", e.target, pointer, err)
	}
	e.doc = out
	return nil
}

// remove takes ctxloom's value at pointer out; a value there that is neither
// what ctxloom put nor an entry that runs ctxloom is the user's edit.
func (e *editor) remove(pointer string, was any) error {
	n, ok, err := e.node(pointer)
	if err != nil || !ok {
		return err
	}
	if !same(n, was) && !confpatch.OwnedBy(n, ctxloomOwner) {
		return &NotOursError{Target: e.target, Pointer: pointer}
	}
	return e.apply(pointer, func(s *hew.Sel) { s.Remove() })
}

// set puts want at pointer. The place may hold the value ctxloom put there
// (was), nothing, or an entry that runs ctxloom; anything else is the user's.
// An unchanged claim over a user's edit is left alone. A first claim on a
// place that already holds exactly the wanted value, and that does not run
// ctxloom, is FOUND: it is someone else's value that happens to be ctxloom's
// too, and the last release must leave it. It returns the containers it
// created to hold the value.
func (e *editor) set(pointer string, was any, hadClaim bool, want any) (created []string, found bool, err error) {
	n, ok, err := e.node(pointer)
	if err != nil {
		return nil, false, err
	}
	if ok {
		owned := confpatch.OwnedBy(n, ctxloomOwner)
		if same(n, want) {
			return nil, !hadClaim && !owned, nil
		}
		if hadClaim && reflect.DeepEqual(was, want) {
			return nil, false, nil
		}
		if ours := owned || (hadClaim && same(n, was)); !ours {
			return nil, false, &NotOursError{Target: e.target, Pointer: pointer}
		}
		return nil, false, e.apply(pointer, func(s *hew.Sel) { s.Set(want) })
	}
	segs := splitPointer(pointer)
	depth := len(segs) - 1
	for ; depth > 0; depth-- {
		if _, ok, err := e.node(joinPointer(segs[:depth])); err != nil {
			return nil, false, err
		} else if ok {
			break
		}
	}
	// segs[:depth] exists; build the missing containers and the value whole.
	value := want
	for i := len(segs) - 1; i > depth; i-- {
		value = map[string]any{segs[i]: value}
		created = append(created, joinPointer(segs[:i]))
	}
	return created, false, e.apply(joinPointer(segs[:depth+1]), func(s *hew.Sel) { s.Set(value) })
}

// prune removes each container ctxloom created that is now empty, deepest
// first, and returns the containers it created that still stand. A container
// a claim sits under is not empty: every claim has been set by now.
func (e *editor) prune(containers []string) []string {
	sorted := slices.Clone(containers)
	sort.Slice(sorted, func(i, j int) bool { return len(splitPointer(sorted[i])) > len(splitPointer(sorted[j])) })
	var keep []string
	for _, c := range sorted {
		n, ok, err := e.node(c)
		if err != nil || !ok {
			continue
		}
		if n.Len() > 0 {
			keep = append(keep, c)
			continue
		}
		if err := e.apply(c, func(s *hew.Sel) { s.Remove() }); err != nil {
			e.failed = err
			return containers
		}
	}
	sort.Strings(keep)
	return keep
}

// same reports whether node holds v, compared as decoded values.
func same(node hew.Node, v any) bool {
	var got any
	if err := node.Value().Decode(&got); err != nil {
		return false
	}
	want, err := canon(v)
	if err != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}

// canon is v as hew decodes it, so a value from the caller, one read back from
// the record and one read out of the file compare alike.
func canon(v any) (any, error) {
	hv, err := hew.ValueOf(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := hv.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func splitPointer(pointer string) []string {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(p, "~1", "/"), "~0", "~")
	}
	return parts
}

func joinPointer(segs []string) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString("/")
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

// pointerPath is pointer as a hew path built from typed key segments, so no
// key is ever parsed as path syntax.
func pointerPath(pointer string) (hew.Path, error) {
	if !strings.HasPrefix(pointer, "/") || pointer == "/" {
		return hew.Path{}, fmt.Errorf("%q is not a pointer to a member", pointer)
	}
	segs := splitPointer(pointer)
	args := make([]hew.SegmentArg, len(segs))
	for i, s := range segs {
		args[i] = hew.Key(s)
	}
	return hew.NewPath(args...), nil
}

func unionKeys(a, b map[string]claimEntry) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		if di, dj := len(splitPointer(out[i])), len(splitPointer(out[j])); di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

func appendNew(list []string, add ...string) []string {
	for _, a := range add {
		if !slices.Contains(list, a) {
			list = append(list, a)
		}
	}
	return list
}

// Paths lists target's claimed places, sorted, each with its writers
// effective first and whether the file on fs holds the effective value.
func (c *Claims) Paths(fs afero.Fs, target string) ([]PathState, error) {
	rec, _, err := c.load(target)
	if err != nil {
		return nil, err
	}
	cur, exists, err := confpatch.ReadTarget(fs, target)
	if err != nil {
		return nil, err
	}
	var doc hew.Document
	if binding, _, ok := bindingFor(target); ok && exists {
		if d, derr := binding.Document(target, cur); derr == nil {
			doc = d
		}
	}
	out := make([]PathState, 0, len(rec.Paths))
	for _, p := range slices.Sorted(mapsKeys(rec.Paths)) {
		entries := ordered(rec.Paths[p])
		st := PathState{Pointer: p}
		for _, e := range entries {
			st.Writers = append(st.Writers, delivery.Writer(e.Writer))
		}
		top := entries[0]
		switch {
		case p == "":
			st.Live = exists && bytes.Equal(cur, top.Bytes)
		case doc != nil:
			if n, ok := confpatch.NodeAt(doc.Root(), p); ok {
				st.Live = same(n, top.Value)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

func mapsKeys(m map[string][]claimEntry) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// Targets lists the files w claims anything in, sorted.
func (c *Claims) Targets(w delivery.Writer) ([]string, error) {
	var out []string
	err := c.each(func(rec claimsRecord) {
		for _, entries := range rec.Paths {
			if slices.ContainsFunc(entries, func(e claimEntry) bool { return e.Writer == string(w) }) {
				out = append(out, rec.Target)
				return
			}
		}
	})
	sort.Strings(out)
	return out, err
}

// Writers lists every writer that claims anything in any file, sorted.
func (c *Claims) Writers() ([]delivery.Writer, error) {
	seen := map[delivery.Writer]bool{}
	err := c.each(func(rec claimsRecord) {
		for _, entries := range rec.Paths {
			for _, e := range entries {
				seen[delivery.Writer(e.Writer)] = true
			}
		}
	})
	out := make([]delivery.Writer, 0, len(seen))
	for w := range seen {
		out = append(out, w)
	}
	slices.Sort(out)
	return out, err
}

func (c *Claims) each(visit func(claimsRecord)) error {
	entries, err := afero.ReadDir(c.fs, c.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), claimsSuffix) {
			continue
		}
		data, err := afero.ReadFile(c.fs, filepath.Join(c.dir, e.Name()))
		if err != nil {
			return err
		}
		var rec claimsRecord
		if err := yamlv3.Unmarshal(data, &rec); err != nil {
			return fmt.Errorf("fsstatic: read claims record %s: %w", e.Name(), err)
		}
		visit(rec)
	}
	return nil
}

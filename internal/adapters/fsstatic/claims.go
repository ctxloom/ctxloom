package fsstatic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
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
	"github.com/ctxloom/ctxloom/internal/core/present"
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

func (rec *claimsRecord) stage(w delivery.Writer, claims []stagedClaim) {
	rec.Seq++
	if rec.Paths == nil {
		rec.Paths = map[string][]claimEntry{}
	}
	for _, cl := range claims {
		e := claimEntry{Writer: string(w), Via: cl.via, Seq: rec.Seq, Value: cl.value, Bytes: cl.bytes}
		rec.Paths[cl.key] = append(dropWriter(rec.Paths[cl.key], string(w), nil, cl.key), e)
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
	claims []stagedClaim // nil for a release
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
func (s *Staging) Stage(target string, w delivery.Writer, claims []present.Claim) error {
	if w == "" || strings.TrimSpace(target) == "" {
		return errors.New("fsstatic: a stage needs a target and a writer")
	}
	staged := make([]stagedClaim, 0, len(claims))
	seen := map[string]bool{}
	opaque := false
	for _, cl := range claims {
		sc, err := stageable(target, cl)
		if err != nil {
			return err
		}
		if seen[sc.key] {
			return fmt.Errorf("fsstatic: %s: %s is claimed twice in one stage", target, cl.Pointer)
		}
		seen[sc.key] = true
		opaque = opaque || isOpaque(sc.key)
		staged = append(staged, sc)
	}
	if opaque && len(staged) > 1 {
		return fmt.Errorf("fsstatic: %s: a whole-file or appended-section claim cannot share a stage with another claim", target)
	}
	t := s.touch(target)
	t.ops = append(t.ops, claimOp{writer: w, claims: staged, stage: true})
	return nil
}

// stagedClaim is a claim checked and keyed for the record: an element is
// keyed by its array and its value, everything else by its pointer.
type stagedClaim struct {
	key, via string
	value    any
	bytes    []byte
}

func isOpaque(key string) bool { return key == "" || key == present.AppendedSection }

func stageable(target string, cl present.Claim) (stagedClaim, error) {
	sc := stagedClaim{key: cl.Pointer, via: cl.Via}
	if isOpaque(cl.Pointer) {
		b, ok := cl.Value.([]byte)
		if !ok {
			return stagedClaim{}, fmt.Errorf("fsstatic: %s: a whole-file or appended-section claim's value is its bytes", target)
		}
		sc.bytes = b
		return sc, nil
	}
	if _, _, ok := bindingFor(target); !ok {
		return stagedClaim{}, fmt.Errorf("fsstatic: %s: claims %s, but no format hew reads names this file", target, cl.Pointer)
	}
	container, elem := strings.CutSuffix(cl.Pointer, "/-")
	if err := validPointer(container); err != nil {
		return stagedClaim{}, fmt.Errorf("fsstatic: %s: %w", target, err)
	}
	v, err := canon(cl.Value)
	if err != nil {
		return stagedClaim{}, fmt.Errorf("fsstatic: %s: the value claimed at %s: %w", target, cl.Pointer, err)
	}
	sc.value = v
	if elem {
		sc.key = elementKey(container, v)
	}
	return sc, nil
}

// elementKey keys an array element by its array and the digest of its
// canonical value: the same element claimed by two writers is one place.
func elementKey(container string, v any) string {
	j, _ := json.Marshal(v) // canon's output: maps, slices and scalars only
	sum := sha256.Sum256(j)
	return container + elementInfix + hex.EncodeToString(sum[:])
}

const elementInfix = "/-/"

// elementOf is the array an element key names; ok is false for any other key.
func elementOf(key string) (container string, ok bool) {
	i := strings.LastIndex(key, elementInfix)
	if i < 0 || len(key)-i-len(elementInfix) != sha256.Size*2 {
		return "", false
	}
	return key[:i], true
}

// claimPointer is a record key as the claim's pointer: an element's is its
// array's append position.
func claimPointer(key string) string {
	if container, ok := elementOf(key); ok {
		return container + "/-"
	}
	return key
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
	opaque := map[string]bool{}
	for _, values := range []map[string]claimEntry{from.Values, to.Values} {
		for k := range values {
			if isOpaque(k) {
				opaque[k] = true
			}
		}
	}
	switch {
	case len(opaque) > 1 || len(opaque) == 1 && len(unionKeys(from.Values, to.Values)) > 1:
		return nil, false, fmt.Errorf("fsstatic: %s: writers claim it in different ways (whole, by section, by place); refusing to guess which wins", t.target)
	case opaque[""]:
		return t.wholeFile(cur, exists, from, to, rec)
	case opaque[present.AppendedSection]:
		return t.section(cur, exists, from, to, rec)
	case len(from.Values) == 0 && len(to.Values) == 0 && len(to.Containers) == 0:
		return cur, exists, nil
	}
	return t.structured(cur, exists, from, to, rec)
}

// section is the transition of text appended after the file's own: the
// section ctxloom put there is found at the end, the user's text before it
// kept, and the effective section appended in its place.
func (t *targetOps) section(cur []byte, exists bool, from, to fileState, rec *claimsRecord) ([]byte, bool, error) {
	o, hasO := from.Values[present.AppendedSection]
	n, hasN := to.Values[present.AppendedSection]
	user := cur
	if hasO && exists {
		u, ok := stripSection(cur, o.Bytes)
		switch {
		case ok:
			user = u
		case hasN && bytes.Equal(o.Bytes, n.Bytes):
			return cur, exists, nil // an unchanged claim the user has since edited: left alone
		default:
			return nil, false, &NotOursError{Target: t.target, Pointer: present.AppendedSection}
		}
	}
	if hasN {
		if !hasO && len(bytes.TrimSpace(user)) == 0 {
			rec.Created = true // nothing of the user's to keep: the file leaves with the section
		}
		return appendSection(user, n.Bytes), true, nil
	}
	if !exists {
		return cur, exists, nil
	}
	if rec.Created && len(bytes.TrimSpace(user)) == 0 {
		rec.Created = false
		return nil, false, nil
	}
	return user, true, nil
}

// appendSection is safefs.AppendSection's layout: the file's own text, a
// blank line, the section, one trailing newline.
func appendSection(user, sec []byte) []byte {
	out := bytes.TrimRight(slices.Clone(user), "\n")
	if len(out) > 0 {
		out = append(out, '\n', '\n')
	}
	out = append(out, bytes.TrimRight(sec, "\n")...)
	return append(out, '\n')
}

// stripSection is the user's text before sec at the end of cur, with the
// trailing newline the layout took; ok is false when cur does not end with it.
func stripSection(cur, sec []byte) ([]byte, bool) {
	tail := append(bytes.TrimRight(slices.Clone(sec), "\n"), '\n')
	if bytes.Equal(cur, tail) {
		return nil, true
	}
	sep := append([]byte("\n\n"), tail...)
	if !bytes.HasSuffix(cur, sep) {
		return nil, false
	}
	return append(slices.Clone(cur[:len(cur)-len(sep)]), '\n'), true
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
		remove := e.remove
		if container, ok := elementOf(p); ok {
			remove = func(_ string, v any) error { return e.removeElement(container, v) }
		}
		if err := remove(p, from.Values[p].Value); err != nil {
			return nil, false, err
		}
	}
	for _, p := range pointers {
		n, hasN := to.Values[p]
		if !hasN {
			continue
		}
		o, hasO := from.Values[p]
		set := e.set
		if container, ok := elementOf(p); ok {
			set = func(_ string, _ any, had bool, v any) ([]string, bool, error) { return e.setElement(container, had, v) }
		}
		created, found, err := set(p, o.Value, hasO, n.Value)
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

// at locates pointer in the current document: the node, and the hew path that
// addresses it (a selected element by its index, never by its selector).
func (e *editor) at(pointer string) (hew.Node, hew.Path, bool, error) {
	d, err := e.binding.Document(e.target, e.doc)
	if err != nil {
		return nil, hew.Path{}, false, fmt.Errorf("fsstatic: %s does not parse: %w", e.target, err)
	}
	n, args, ok, err := locate(d.Root(), parsePointer(pointer))
	if errors.Is(err, errNotAnArray) {
		return nil, hew.Path{}, false, &NotOursError{Target: e.target, Pointer: pointer}
	}
	return n, hew.NewPath(args...), ok, err
}

func (e *editor) node(pointer string) (hew.Node, bool, error) {
	n, _, ok, err := e.at(pointer)
	return n, ok, err
}

func (e *editor) applyAt(p hew.Path, op func(*hew.Sel)) error {
	d, err := hew.OpenBytes(e.target, e.doc, hew.As(e.format))
	if err != nil {
		return fmt.Errorf("fsstatic: %s does not parse: %w", e.target, err)
	}
	op(d.AtPath(p))
	out, err := d.Bytes()
	if err != nil {
		return fmt.Errorf("fsstatic: %s: %s: %w", e.target, p.String(), err)
	}
	e.doc = out
	return nil
}

// remove takes ctxloom's value at pointer out; a value there that is neither
// what ctxloom put nor an entry that runs ctxloom is the user's edit.
func (e *editor) remove(pointer string, was any) error {
	n, p, ok, err := e.at(pointer)
	if err != nil || !ok {
		return err
	}
	if !same(n, was) && !confpatch.OwnedBy(n, ctxloomOwner) {
		return &NotOursError{Target: e.target, Pointer: pointer}
	}
	return e.applyAt(p, func(s *hew.Sel) { s.Remove() })
}

// set puts want at pointer. The place may hold the value ctxloom put there
// (was), nothing, or an entry that runs ctxloom; anything else is the user's.
// An unchanged claim over a user's edit is left alone. A first claim on a
// place that already holds exactly the wanted value, and that does not run
// ctxloom, is FOUND: it is someone else's value that happens to be ctxloom's
// too, and the last release must leave it. It returns the containers it
// created to hold the value.
func (e *editor) set(pointer string, was any, hadClaim bool, want any) (created []string, found bool, err error) {
	n, p, ok, err := e.at(pointer)
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
		return nil, false, e.applyAt(p, func(s *hew.Sel) { s.Set(want) })
	}
	return e.create(parsePointer(pointer), want)
}

// create builds what is missing of segs and puts want at its end: from the
// deepest place that stands, a member is set or a selected element appended,
// holding every missing container down to want. It returns those containers.
func (e *editor) create(segs []pseg, want any) ([]string, bool, error) {
	k := len(segs) - 1
	var base hew.Path
	for ; k >= 0; k-- {
		_, p, ok, err := e.at(joinSegs(segs[:k]))
		if k == 0 {
			p, ok, err = hew.NewPath(), true, nil
		}
		if err != nil {
			return nil, false, err
		}
		if ok {
			base = p
			break
		}
	}
	var created []string
	for j := k + 1; j < len(segs); j++ {
		created = append(created, joinSegs(segs[:j]))
	}
	value, err := build(segs[k:], want)
	if err != nil {
		return nil, false, err
	}
	if s := segs[k]; s.sel {
		el, err := withSelector(s, value)
		if err != nil {
			return nil, false, err
		}
		return created, false, e.applyAt(base, func(sel *hew.Sel) { sel.Add(el) })
	}
	return created, false, e.applyAt(base.Append(hew.Key(segs[k].key).(hew.Segment)), func(sel *hew.Sel) { sel.Set(value) })
}

// build is the value the place rest[0] names holds so that want sits at the
// end of rest: a map for a member below, an array holding the selected
// element for a selector below.
func build(rest []pseg, want any) (any, error) {
	if len(rest) == 1 {
		return want, nil
	}
	child, err := build(rest[1:], want)
	if err != nil {
		return nil, err
	}
	if next := rest[1]; next.sel {
		el, err := withSelector(next, child)
		if err != nil {
			return nil, err
		}
		return []any{el}, nil
	}
	return map[string]any{rest[1].key: child}, nil
}

// withSelector is v made selectable by s: the selector's field set to its
// value, or left out when the value is empty (what an empty value selects).
func withSelector(s pseg, v any) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("fsstatic: %s selects an object, and what is claimed there is not one", s)
	}
	out := maps.Clone(m)
	if s.value != "" {
		out[s.field] = s.value
	}
	return out, nil
}

// elementIndex finds the element of the array at container that holds v.
func (e *editor) elementIndex(container string, v any) (int, hew.Node, hew.Path, error) {
	n, p, ok, err := e.at(container)
	if err != nil || !ok {
		return -1, nil, p, err
	}
	if n.Kind() != hew.KindSeq {
		return -1, nil, p, &NotOursError{Target: e.target, Pointer: container}
	}
	for i := 0; i < n.Len(); i++ {
		if el, ok := n.Elem(i); ok && same(el, v) {
			return i, el, p, nil
		}
	}
	return -1, nil, p, nil
}

// removeElement takes the element holding v out of the array; one that is no
// longer there was taken out by someone else.
func (e *editor) removeElement(container string, v any) error {
	i, _, p, err := e.elementIndex(container, v)
	if err != nil || i < 0 {
		return err
	}
	return e.applyAt(p.Append(hew.Index(i).(hew.Segment)), func(s *hew.Sel) { s.Remove() })
}

// setElement appends v to the array at container unless an element already
// holds it: one that is ctxloom's own is taken over, any other is FOUND on a
// first claim. A missing array is created holding v.
func (e *editor) setElement(container string, hadClaim bool, v any) ([]string, bool, error) {
	if _, ok, err := e.node(container); err != nil {
		return nil, false, err
	} else if !ok {
		created, _, err := e.create(parsePointer(container), []any{v})
		return append(created, container), false, err
	}
	i, el, p, err := e.elementIndex(container, v)
	if err != nil {
		return nil, false, err
	}
	if i >= 0 {
		return nil, !hadClaim && !ownedElement(el), nil
	}
	return nil, false, e.applyAt(p, func(s *hew.Sel) { s.Add(v) })
}

// ownedElement reports whether an array element is ctxloom's own: an entry
// that runs ctxloom (a hook), or a hook group every hook of which does.
func ownedElement(n hew.Node) bool {
	if confpatch.OwnedBy(n, ctxloomOwner) {
		return true
	}
	if n.Kind() != hew.KindMap {
		return false
	}
	hooks, ok := n.Member("hooks")
	if !ok || hooks.Kind() != hew.KindSeq {
		return false
	}
	for i := 0; i < hooks.Len(); i++ {
		if h, ok := hooks.Elem(i); !ok || !confpatch.OwnedBy(h, ctxloomOwner) {
			return false
		}
	}
	return true
}

// prune removes each container ctxloom created that is now empty, deepest
// first, and returns the containers it created that still stand. A selected
// element holding nothing but its selector is empty. A container a claim sits
// under is not empty: every claim has been set by now.
func (e *editor) prune(containers []string) []string {
	sorted := slices.Clone(containers)
	sort.Slice(sorted, func(i, j int) bool { return len(parsePointer(sorted[i])) > len(parsePointer(sorted[j])) })
	var keep []string
	for _, c := range sorted {
		n, p, ok, err := e.at(c)
		if err != nil || !ok {
			continue
		}
		if !emptied(n, parsePointer(c)) {
			keep = append(keep, c)
			continue
		}
		if err := e.applyAt(p, func(s *hew.Sel) { s.Remove() }); err != nil {
			e.failed = err
			return containers
		}
	}
	sort.Strings(keep)
	return keep
}

// emptied reports whether a created container holds nothing anyone put
// there: no member, or only the selector that made it selectable.
func emptied(n hew.Node, segs []pseg) bool {
	size := n.Len()
	if last := segs[len(segs)-1]; last.sel {
		if _, ok := n.Member(last.field); ok {
			size--
		}
	}
	return size == 0
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

// pseg is one segment of a claim pointer: an RFC 6901 key, or a SELECTOR
// field=value naming the first element of an array whose field holds value —
// an element without the field is selected by the empty value. "~2" escapes a
// "=" in a key, as hew's own paths do.
type pseg struct {
	key          string
	sel          bool
	field, value string
}

func (s pseg) String() string {
	if s.sel {
		return present.PointerSelect(s.field, s.value)
	}
	return present.PointerKey(s.key)
}

func parsePointer(pointer string) []pseg {
	if pointer == "" {
		return nil
	}
	raw := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	out := make([]pseg, len(raw))
	for i, r := range raw {
		if field, value, ok := strings.Cut(r, "="); ok {
			out[i] = pseg{sel: true, field: present.UnescapeSegment(field), value: present.UnescapeSegment(value)}
			continue
		}
		out[i] = pseg{key: present.UnescapeSegment(r)}
	}
	return out
}

func joinSegs(segs []pseg) string {
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.String())
	}
	return b.String()
}

// errNotAnArray is a selector meeting something that is not an array.
var errNotAnArray = errors.New("a selector names an element of something that is not an array")

// locate walks segs down from root: a key through map members, a selector to
// the first element it selects. It returns the node and the hew segments that
// address it; ok is false when a place on the way is missing.
func locate(root hew.Node, segs []pseg) (hew.Node, []hew.SegmentArg, bool, error) {
	n := root
	var args []hew.SegmentArg
	for _, s := range segs {
		if !s.sel {
			next, ok := n.Member(s.key) // a key into anything but a map is absent

			if !ok {
				return nil, args, false, nil
			}
			n, args = next, append(args, hew.Key(s.key))
			continue
		}
		if n.Kind() != hew.KindSeq {
			return nil, args, false, errNotAnArray
		}
		i := selected(n, s)
		if i < 0 {
			return nil, args, false, nil
		}
		n, _ = n.Elem(i)
		args = append(args, hew.Index(i))
	}
	return n, args, true, nil
}

// selected is the index of the first element of seq s selects, or -1.
func selected(seq hew.Node, s pseg) int {
	for i := 0; i < seq.Len(); i++ {
		el, ok := seq.Elem(i)
		if !ok || el.Kind() != hew.KindMap {
			continue
		}
		f, ok := el.Member(s.field)
		if !ok {
			if s.value == "" {
				return i
			}
			continue
		}
		var v string
		if f.Kind() == hew.KindScalar && f.Value().Decode(&v) == nil && v == s.value {
			return i
		}
	}
	return -1
}

// validPointer refuses a pointer that names no member.
func validPointer(pointer string) error {
	if !strings.HasPrefix(pointer, "/") || pointer == "/" {
		return fmt.Errorf("%q is not a pointer to a member", pointer)
	}
	return nil
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
		if di, dj := len(parsePointer(out[i])), len(parsePointer(out[j])); di != dj {
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
		st.Pointer = claimPointer(p)
		switch container, elem := elementOf(p); {
		case p == "":
			st.Live = exists && bytes.Equal(cur, top.Bytes)
		case p == present.AppendedSection:
			_, st.Live = stripSection(cur, top.Bytes)
		case doc != nil && elem:
			st.Live = elementLive(doc, container, top.Value)
		case doc != nil:
			if n, _, ok, _ := locate(doc.Root(), parsePointer(p)); ok {
				st.Live = same(n, top.Value)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

func elementLive(doc hew.Document, container string, v any) bool {
	n, _, ok, _ := locate(doc.Root(), parsePointer(container))
	if !ok || n.Kind() != hew.KindSeq {
		return false
	}
	for i := 0; i < n.Len(); i++ {
		if el, ok := n.Elem(i); ok && same(el, v) {
			return true
		}
	}
	return false
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

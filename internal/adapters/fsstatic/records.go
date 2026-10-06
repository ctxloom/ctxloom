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
	"strconv"
	"strings"
	"unicode/utf8"

	hew "github.com/benjaminabbitt/hew/go"
	"github.com/spf13/afero"
	yaml "gopkg.in/yaml.v3"
	"mvdan.cc/sh/v3/shell"

	// The formats a claim's place is found in: registered HERE, by the
	// record that needs them, so a file's claims never depend on which
	// engine happens to be linked into the binary.
	_ "github.com/benjaminabbitt/hew/go/ext/json"
	_ "github.com/benjaminabbitt/hew/go/ext/toml"
	_ "github.com/benjaminabbitt/hew/go/ext/yaml"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/exectoken"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/schemaver"
	"github.com/ctxloom/ctxloom/internal/shared/upgrade"
	"github.com/ctxloom/ctxloom/internal/shared/yamlx"
)

// Records is the ownership record: one record per TARGET FILE, naming for each
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
type Records struct {
	fs  afero.Fs
	dir string
}

var _ delivery.Ownership = (*Records)(nil)

// ownershipSuffix names the per-writer record this one replaced: deleted by
// its exact name on the first write to its target.
const ownershipSuffix = ".ownership.yaml"

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

// claimsKind versions a claims record. LegacyKey: the record spelled its
// format generation `claims` before schemaver.
var claimsKind = schemaver.Kind{Name: "claims record", LegacyKey: "claims", Oldest: 2, Steps: []upgrade.Upgrader{contentAsText{}}}

// contentAsText is generation 3: a whole-file or appended-section claim's
// delivered bytes move from `bytes`, one YAML integer per byte, to `content`,
// the bytes as a string — text when they are UTF-8, !!binary when not — in
// every claim and in the pending note's prior state.
type contentAsText struct{}

func (contentAsText) Name() string { return "store delivered bytes as content" }

func (contentAsText) Apply(root *yaml.Node) bool {
	changed := false
	if paths := yamlx.MapValue(root, "paths"); paths != nil && paths.Kind == yaml.MappingNode {
		for i := 1; i < len(paths.Content); i += 2 {
			for _, e := range paths.Content[i].Content {
				changed = bytesToContent(e) || changed
			}
		}
	}
	values := yamlx.MapValue(yamlx.MapValue(yamlx.MapValue(root, "pending"), "prior"), "values")
	if values != nil && values.Kind == yaml.MappingNode {
		for i := 1; i < len(values.Content); i += 2 {
			changed = bytesToContent(values.Content[i]) || changed
		}
	}
	return changed
}

// bytesToContent rewrites one claim entry's integer-list `bytes` as
// `content`. A list that is not bytes is left as it is: the claim then holds
// no content, and so matches no file, which refuses rather than overwrites.
func bytesToContent(entry *yaml.Node) bool {
	if entry.Kind != yaml.MappingNode {
		return false
	}
	list := yamlx.MapValue(entry, "bytes")
	if list == nil || list.Kind != yaml.SequenceNode {
		return false
	}
	b := make([]byte, len(list.Content))
	for i, n := range list.Content {
		v, err := strconv.ParseUint(n.Value, 10, 8)
		if n.Kind != yaml.ScalarNode || err != nil {
			return false
		}
		b[i] = byte(v)
	}
	var content yaml.Node
	if err := content.Encode(deliveredContent(b)); err != nil {
		return false
	}
	yamlx.MapDelete(entry, "bytes")
	yamlx.MapSet(entry, "content", &content)
	return true
}

const (
	claimsSuffix = ".claims.yaml"
	// ctxloomOwner is the executable basename that proves an unclaimed entry
	// is ctxloom's own (confpatch.OwnedBy).
	ctxloomOwner = "ctxloom"
)

// NewRecords opens the record store at dir on fs. dir is created on the first
// record written; an existing one is tightened to owner-only.
func NewRecords(recordFS afero.Fs, dir string) (*Records, error) {
	if recordFS == nil {
		return nil, errors.New("fsstatic: nil record filesystem")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("fsstatic: empty record directory")
	}
	c := &Records{fs: recordFS, dir: dir}
	return c, c.Prepare(context.Background())
}

// Prepare tightens an EXISTING record dir to owner-only (delivery.Ownership's
// security invariant); a missing one is left missing.
func (c *Records) Prepare(context.Context) error {
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
	SchemaVersion int                     `yaml:"schema_version"`
	Target        string                  `yaml:"target"`
	Created       bool                    `yaml:"created"`
	Containers    []string                `yaml:"containers,omitempty"`
	Found         []string                `yaml:"found,omitempty"`
	Seq           uint64                  `yaml:"seq"`
	Paths         map[string][]claimEntry `yaml:"paths"`
	Pending       *pendingWrite           `yaml:"pending,omitempty"`
}

// claimEntry is one writer's claim at one place. Seq orders claims of one
// writer kind: the latest staged is on top. Content is a whole-file or
// appended-section claim's delivered bytes.
type claimEntry struct {
	Writer  string           `yaml:"writer"`
	Via     string           `yaml:"via,omitempty"`
	Seq     uint64           `yaml:"seq"`
	Value   any              `yaml:"value"`
	Content deliveredContent `yaml:"content,omitempty"`
}

// deliveredContent is delivered bytes held as a string, so the record
// carries text as text. UTF-8 is written DOUBLE-QUOTED, never as a block
// scalar: yaml.v3 writes a block scalar for text that starts with a newline
// or a space that it then cannot read back. Bytes that are not UTF-8 are
// written as yaml.v3 writes such a string, !!binary, and read back exactly.
type deliveredContent string

func (c deliveredContent) MarshalYAML() (any, error) {
	if !utf8.ValidString(string(c)) {
		return string(c), nil
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: string(c)}, nil
}

// pendingWrite is written WITH the record, before the target: the target's
// digest before and after the write, and the state the file was in before
// it. It is dropped once the write is confirmed (targetOps.confirm); a note
// the next load still finds tells from the target's digest whether its write
// landed.
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

func (c *Records) path(target string) string {
	return filepath.Join(c.dir, confpatch.RecordPrefix(target)+claimsSuffix)
}

func (c *Records) load(target string) (claimsRecord, []byte, error) {
	data, err := afero.ReadFile(c.fs, c.path(target))
	if os.IsNotExist(err) {
		return claimsRecord{SchemaVersion: claimsKind.Current(), Target: target}, nil, nil
	}
	if err != nil {
		return claimsRecord{}, nil, err
	}
	rec, err := decodeClaims(c.path(target), data)
	if err != nil {
		return claimsRecord{}, nil, err
	}
	return rec, data, nil
}

// decodeClaims reads one record's bytes at the current generation, migrating
// an older spelling in memory only: the next seal writes it stamped.
func decodeClaims(path string, data []byte) (claimsRecord, error) {
	var rec claimsRecord
	if _, err := claimsKind.Decode(data, &rec); err != nil {
		return claimsRecord{}, fmt.Errorf("fsstatic: claims record %s: %w", path, err)
	}
	return rec, nil
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
		e := claimEntry{Writer: string(w), Via: cl.via, Seq: rec.Seq, Value: cl.value, Content: cl.content}
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
	return a.Via == b.Via && a.Content == b.Content && reflect.DeepEqual(a.Value, b.Value)
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
	c       *Records
	b       *safefs.Batch
	targets map[string]*targetOps
}

// In is a staging bound to b.
func (c *Records) In(b *safefs.Batch) delivery.Staging {
	return &Staging{c: c, b: b, targets: map[string]*targetOps{}}
}

type claimOp struct {
	writer delivery.Writer
	claims []stagedClaim // nil for a release
	keep   func(pointer, via string) bool
	stage  bool
}

type targetOps struct {
	c      *Records
	target string
	ops    []claimOp
	// set by the fold for the seal
	rec   claimsRecord
	disk  []byte
	prior fileState
	had   []string // the writers the record named before the ops
	// set by the seal when it notes a write pending, for the confirm
	noted *claimsRecord
}

func (s *Staging) touch(target string) *targetOps {
	t, ok := s.targets[target]
	if !ok {
		t = &targetOps{c: s.c, target: target}
		s.targets[target] = t
		s.b.Edit(target, t.fold)
		s.b.Seal(target, t.seal)
		s.b.Confirm(target, t.confirm)
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
	content  deliveredContent
}

func isOpaque(key string) bool { return key == "" || key == present.AppendedSection }

func stageable(target string, cl present.Claim) (stagedClaim, error) {
	sc := stagedClaim{key: cl.Pointer, via: cl.Via}
	if isOpaque(cl.Pointer) {
		b, ok := cl.Value.([]byte)
		if !ok {
			return stagedClaim{}, fmt.Errorf("fsstatic: %s: a whole-file or appended-section claim's value is its bytes", target)
		}
		sc.content = deliveredContent(b)
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
	t.disk, t.prior, t.had = disk, rec.state(), rec.writers()
	// A note found here was never confirmed. Its write either landed, and the
	// note goes, or did not, and the write it describes is redone when the
	// file still stands as it was before. A note that outlived its write
	// would mistake a user's exact revert for that write being lost.
	if p := rec.Pending; p != nil {
		rec.Pending = nil
		if now := digest(cur, exists); now != p.After && now == p.Before {
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

// seal writes the record before the target, its writers' claimant markers
// before the record and the departed writers' out after it (see
// claimantsDir).
func (t *targetOps) seal(before []byte, existed bool, after []byte, keep bool) error {
	now := t.rec.writers()
	for _, w := range now {
		if err := t.c.mark(w, t.target); err != nil {
			return err
		}
	}
	if err := t.writeRecord(before, existed, after, keep); err != nil {
		return err
	}
	gone := slices.Clone(t.had)
	for _, op := range t.ops {
		gone = append(gone, string(op.writer))
	}
	for _, w := range gone {
		if !slices.Contains(now, w) {
			if err := t.c.unmark(w, t.target); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeRecord writes the record: with a pending note when the target is
// about to change, and not at all when nothing in it changed. A record left
// with no claims is removed once its target is settled — kept, with its note,
// while the write that emptied it may not have landed. The records this one
// superseded are retired (retireSuperseded).
func (t *targetOps) writeRecord(before []byte, existed bool, after []byte, keep bool) error {
	rec := t.rec
	changing := digest(before, existed) != digest(after, keep)
	if changing {
		rec.Pending = &pendingWrite{Before: digest(before, existed), After: digest(after, keep), Prior: t.prior}
		t.noted = &rec
	}
	if err := t.c.retireSuperseded(t.target); err != nil {
		return err
	}
	path := t.c.path(t.target)
	if len(rec.Paths) == 0 && !changing {
		if err := t.c.fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	rec.SchemaVersion, rec.Target = claimsKind.Current(), t.target
	data, err := yaml.Marshal(rec)
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

// confirm drops the note the seal wrote, once the target's write has landed:
// the record then says only what is claimed, and a record left with no claims
// goes.
func (t *targetOps) confirm() error {
	if t.noted == nil {
		return nil
	}
	rec := *t.noted
	rec.Pending = nil
	path := t.c.path(t.target)
	if len(rec.Paths) == 0 {
		if err := t.c.fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	data, err := yaml.Marshal(rec)
	if err != nil {
		return err
	}
	return safefs.WriteFile(t.c.fs, path, data, owneronly.FileMode, safefs.Durable())
}

// retireSuperseded deletes the records this one superseded for target: the
// per-writer ownership record, and claude's confpatch records.
func (c *Records) retireSuperseded(target string) error {
	if err := c.dropOldRecord(target); err != nil {
		return err
	}
	return c.retireConfpatchRecords(target)
}

// dropOldRecord deletes the per-writer ownership record this record replaces,
// recognized by its exact name.
func (c *Records) dropOldRecord(target string) error {
	old := filepath.Join(c.dir, confpatch.RecordPrefix(target)+ownershipSuffix)
	if err := c.fs.Remove(old); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsstatic: remove the superseded record %s: %w", old, err)
	}
	return nil
}

// retireConfpatchRecords deletes the §9.7 records claude's retired
// settings/MCP writer kept for target in this directory: a confpatch store
// owned by ctxloom, superseded by this record. Nothing reverses them any
// more, and a status that still read one reported an uninstalled server as
// installed.
//
// They share their naming with `config-write`'s audit records, so a file is
// recognised by its CONTENT, never its name: a record of exactly this target
// that carries a reversal. confpatch.Store writes the reversal to undo its
// application; config-write's audit record has no such field. Taskloom's
// store keeps its own subdirectory and is never listed here. A file that
// does not parse as a record is not recognisably claude's, and stays.
func (c *Records) retireConfpatchRecords(target string) error {
	names, err := confpatch.RecordNames(c.fs, c.dir, target)
	if err != nil {
		return err
	}
	for _, n := range names {
		path := filepath.Join(c.dir, n)
		data, err := afero.ReadFile(c.fs, path)
		if err != nil {
			return fmt.Errorf("fsstatic: read %s: %w", path, err)
		}
		var rec confpatch.Record
		if yaml.Unmarshal(data, &rec) != nil || rec.Reversal == "" || !recordsTarget(rec, target) {
			continue
		}
		if err := c.fs.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("fsstatic: retire the confpatch record %s: %w", path, err)
		}
	}
	return nil
}

// recordsTarget reports whether rec describes target.
func recordsTarget(rec confpatch.Record, target string) bool {
	return slices.ContainsFunc(rec.Targets, func(rt confpatch.RecordTarget) bool { return rt.Target == target })
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
	kind, err := claimKind(from, to)
	switch {
	case err != nil:
		return nil, false, fmt.Errorf("fsstatic: %s: %w", t.target, err)
	case kind == "":
		return t.wholeFile(cur, exists, from, to, rec)
	case kind == present.AppendedSection:
		return t.section(cur, exists, from, to, rec)
	case len(from.Values) == 0 && len(to.Values) == 0 && len(to.Containers) == 0:
		return cur, exists, nil
	}
	return t.structured(cur, exists, from, to, rec)
}

// byPlace is claimKind's answer for a file claimed place by place.
const byPlace = "/"

// claimKind is how a file is claimed across both states: whole (""), by
// appended section, or byPlace. Whole and section claims stand alone.
func claimKind(from, to fileState) (string, error) {
	keys := unionKeys(from.Values, to.Values)
	opaque := slices.DeleteFunc(slices.Clone(keys), func(k string) bool { return !isOpaque(k) })
	switch {
	case len(opaque) == 0:
		return byPlace, nil
	case len(keys) > 1:
		return "", errors.New("writers claim it in different ways (whole, by section, by place); refusing to guess which wins")
	}
	return opaque[0], nil
}

// section is the transition of text appended after the file's own: the
// section ctxloom put there is found at the end, the user's text before it
// kept, and the effective section appended in its place.
func (t *targetOps) section(cur []byte, exists bool, from, to fileState, rec *claimsRecord) ([]byte, bool, error) {
	o, hasO := from.Values[present.AppendedSection]
	n, hasN := to.Values[present.AppendedSection]
	user, leave, err := t.sectionUser(cur, exists, o, hasO, n, hasN)
	if err != nil || leave {
		return cur, exists, err
	}
	if hasN {
		if !hasO && len(bytes.TrimSpace(user)) == 0 {
			rec.Created = true // nothing of the user's to keep: the file leaves with the section
		}
		return appendSection(user, []byte(n.Content)), true, nil
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

// sectionUser is the user's text before ctxloom's section, found at the end
// of cur. A section the file no longer holds anywhere is a claim it has lost,
// as a whole file deleted or a structured place removed is: all of cur is the
// user's, and the effective section goes back after it. leave is an
// unchanged claim whose section the user has since written after, left
// alone; a section no longer at the end otherwise is not ctxloom's to cut out.
func (t *targetOps) sectionUser(cur []byte, exists bool, o claimEntry, hasO bool, n claimEntry, hasN bool) ([]byte, bool, error) {
	if !hasO || !exists {
		return cur, false, nil
	}
	if u, ok := stripSection(cur, []byte(o.Content)); ok {
		return u, false, nil
	}
	if !bytes.Contains(cur, []byte(strings.TrimRight(string(o.Content), "\n"))) {
		return cur, false, nil
	}
	if hasN && o.Content == n.Content {
		return cur, true, nil
	}
	return nil, false, &NotOursError{Target: t.target, Pointer: present.AppendedSection}
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
		if leave, err := t.wholeFileEdited(cur, o, hasO, n, hasN); err != nil || leave {
			return cur, exists, err
		}
	}
	if hasN {
		if !exists {
			rec.Created = true
		}
		return []byte(n.Content), true, nil
	}
	if !exists || !rec.Created {
		return cur, exists, nil
	}
	rec.Created = false
	return nil, false, nil
}

// wholeFileEdited judges a file that stands against its whole-file claims:
// it holds one of them (ours), or it is an unchanged claim the user has
// since edited (leave it), or it is not ctxloom's.
func (t *targetOps) wholeFileEdited(cur []byte, o claimEntry, hasO bool, n claimEntry, hasN bool) (bool, error) {
	if (hasO && deliveredContent(cur) == o.Content) || (hasN && deliveredContent(cur) == n.Content) {
		return false, nil
	}
	if hasO && hasN && o.Content == n.Content {
		return true, nil
	}
	return false, &NotOursError{Target: t.target}
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
	if err := e.removeAll(pointers, from, to, rec); err != nil {
		return nil, false, err
	}
	if err := e.setAll(pointers, from, to, rec); err != nil {
		return nil, false, err
	}
	if err := e.removeSuperseded(from, to); err != nil {
		return nil, false, err
	}
	rec.Containers = e.prune(rec.Containers)
	if err := e.err(); err != nil {
		return nil, false, err
	}
	return e.settle(cur, exists, doc, to, rec)
}

// removeAll takes out each place no claim holds any longer, deepest first so
// a member leaves before its container is judged. A place whose value
// ctxloom found there is left as found.
func (e *editor) removeAll(pointers []string, from, to fileState, rec *claimsRecord) error {
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
			return err
		}
	}
	return nil
}

// setAll puts each place's effective value, shallowest first, recording the
// containers it creates and the values it finds there already.
func (e *editor) setAll(pointers []string, from, to fileState, rec *claimsRecord) error {
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
			return err
		}
		rec.Containers = appendNew(rec.Containers, created...)
		if found {
			rec.Found = appendNew(rec.Found, p)
		}
	}
	return nil
}

// settle is the file the edits leave: created when it was not there and now
// holds something, removed when ctxloom created it and nothing of anyone's is
// left, untouched when it was not there and still holds nothing.
func (e *editor) settle(cur []byte, exists bool, seed []byte, to fileState, rec *claimsRecord) ([]byte, bool, error) {
	if !exists && !bytes.Equal(e.doc, seed) {
		rec.Created = true
	}
	if rec.Created && len(to.Values) == 0 && bytes.Equal(bytes.TrimSpace(e.doc), bytes.TrimSpace(confpatch.EmptyDocument(e.format))) {
		rec.Created = false
		return nil, false, nil
	}
	if !exists && bytes.Equal(e.doc, seed) {
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

// removeSuperseded takes out of each array ctxloom claims elements in the
// leftovers of its own that no record names: an element that runs ctxloom
// (confpatch.OwnedBy), invokes the same ctxloom subcommand as an element
// claimed into that array now, and holds a value no record knows. A tracked
// settings file carries such entries onto a machine with no record of them,
// and an older spelling of a callback ctxloom still installs (its shell form,
// or another argument set) would otherwise run beside the current one.
//
// The subcommand, not the executable, is the identity: a user's own entry
// running ctxloom with a verb ctxloom does not claim in that array is theirs
// and stays.
//
// It runs AFTER setAll: every array it sweeps then already holds a current
// claim, so it never empties one that an append would have to fill — hew
// mis-renders an append into an emptied multi-line array.
func (e *editor) removeSuperseded(from, to fileState) error {
	known := map[string][]any{}
	claimed := map[string][]string{}
	for _, st := range []fileState{from, to} {
		for key, entry := range st.Values {
			if container, ok := elementOf(key); ok {
				known[container] = append(known[container], entry.Value)
			}
		}
	}
	for key, entry := range to.Values {
		container, ok := elementOf(key)
		if !ok {
			continue
		}
		if sub, ok := ctxloomSubcommand(entry.Value); ok {
			claimed[container] = appendNew(claimed[container], sub)
		}
	}
	for _, container := range slices.Sorted(maps.Keys(claimed)) {
		if err := e.removeSupersededIn(container, known[container], claimed[container]); err != nil {
			return err
		}
	}
	return nil
}

// removeSupersededIn is removeSuperseded for one array, last element first so
// an earlier index stays valid across a removal.
func (e *editor) removeSupersededIn(container string, known []any, claimed []string) error {
	n, p, ok, err := e.at(container)
	if err != nil || !ok || n.Kind() != hew.KindSeq {
		return err
	}
	for i := n.Len() - 1; i >= 0; i-- {
		if el, ok := n.Elem(i); !ok || !superseded(el, known, claimed) {
			continue
		}
		if err := e.applyAt(p.Append(hew.Index(i).(hew.Segment)), func(s *hew.Sel) { s.Remove() }); err != nil {
			return err
		}
	}
	return nil
}

// superseded reports whether el is a leftover of ctxloom's own (see
// removeSuperseded): it runs ctxloom, no record knows its value, and it
// invokes a subcommand claimed into its array now.
func superseded(el hew.Node, known []any, claimed []string) bool {
	if !confpatch.OwnedBy(el, ctxloomOwner) || slices.ContainsFunc(known, func(v any) bool { return same(el, v) }) {
		return false
	}
	var v any
	if el.Value().Decode(&v) != nil {
		return false
	}
	sub, ok := ctxloomSubcommand(v)
	return ok && slices.Contains(claimed, sub)
}

// ctxloomSubcommand is the ctxloom subcommand a hook entry invokes — its
// leading argument words up to the first flag, joined by a space. ok is false
// for an entry that does not run ctxloom or names no subcommand.
func ctxloomSubcommand(v any) (string, bool) {
	args, ok := ctxloomArgs(v)
	if !ok {
		return "", false
	}
	end := slices.IndexFunc(args, func(w string) bool { return strings.HasPrefix(w, "-") })
	if end < 0 {
		end = len(args)
	}
	return strings.Join(args[:end], " "), end > 0
}

// ctxloomArgs is the argument words of a hook entry that runs ctxloom,
// whichever form it is spelled in: claude's exec form (the executable in
// "command", its arguments in "args") or a shell line in "command" alone.
func ctxloomArgs(v any) ([]string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	command, ok := m["command"].(string)
	if !ok || !exectoken.IsManaged(command, ctxloomOwner) {
		return nil, false
	}
	raw, exec := m["args"].([]any)
	if !exec {
		fields, err := shell.Fields(command, func(string) string { return "" })
		if err != nil || len(fields) == 0 {
			return nil, false
		}
		return fields[1:], true
	}
	args := make([]string, 0, len(raw))
	for _, a := range raw {
		w, ok := a.(string)
		if !ok {
			return nil, false
		}
		args = append(args, w)
	}
	return args, true
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
func (c *Records) Paths(fs afero.Fs, target string) ([]delivery.PathState, error) {
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
	out := make([]delivery.PathState, 0, len(rec.Paths))
	for _, p := range slices.Sorted(mapsKeys(rec.Paths)) {
		entries := ordered(rec.Paths[p])
		st := delivery.PathState{Pointer: p}
		for _, e := range entries {
			st.Writers = append(st.Writers, delivery.Writer(e.Writer))
		}
		st.Pointer = claimPointer(p)
		st.Live = live(doc, cur, exists, p, entries[0])
		out = append(out, st)
	}
	return out, nil
}

// live reports whether the file holds the effective claim at key.
func live(doc hew.Document, cur []byte, exists bool, key string, top claimEntry) bool {
	switch container, elem := elementOf(key); {
	case key == "":
		return exists && deliveredContent(cur) == top.Content
	case key == present.AppendedSection:
		_, ok := stripSection(cur, []byte(top.Content))
		return ok
	case doc == nil:
		return false
	case elem:
		return elementLive(doc, container, top.Value)
	}
	n, _, ok, _ := locate(doc.Root(), parsePointer(key))
	return ok && same(n, top.Value)
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

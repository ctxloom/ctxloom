// Package parity is the shared engine behind ctxloom's mirror-parity gates.
//
// ctxloom projects agent.ChatEvent (and its variant structs) onto several
// hand-written mirrors: the canonical transcript's on-disk payloads
// (internal/transcript) and the `--format json` NDJSON DTOs the VSCode
// frontend consumes (internal/cli). Each mirror is edited in three places at
// once — mirror struct, converter, published schema — and every time one of
// those was forgotten the result was SILENT field loss: the writer succeeded,
// the bytes went out, and the field simply was not in them. That has now
// happened on three separate mirrors (U144-F01, U144-F02, and the `--format
// json` entry/session DTOs, found out of corpus by batch 18/19).
//
// The engine turns "someone forgot one of the three" into a test failure. It
// has THREE halves, deliberately different in kind so no one of them can be
// satisfied by the wrong evidence:
//
//  1. STRUCTURAL (CheckFieldCount) — reflect.NumField parity between the agent
//     type and its mirror, modulo an explicit, reasoned exemption map. Catches
//     a field added to the agent type with no mirror at all.
//
//  2. BYTE SURVIVAL (CheckConverterCopies) — every field of a fully-populated
//     agent value is stamped with a value unique to that field, the REAL
//     converter runs, the result is marshalled, and every sentinel must be
//     found among the leaves of the resulting JSON. Catches a field that HAS a
//     mirror but that the converter never copies. It asserts on surviving
//     VALUES, not names: a converter that copies field A into field B's slot
//     still fails, because each sentinel is distinct.
//
//  3. BOOL ISOLATION (CheckBoolIsolation) — half 2 cannot give a bool a
//     distinct token, so it gates bools by COUNT, and a count is satisfiable
//     by the wrong evidence: a converter that copies IsError into BOTH the
//     is_error and sidechain slots still emits two `true` leaves. Half 3
//     closes that: each bool field is set true ALONE, and must produce exactly
//     one true leaf, at a JSON key distinct from every other bool field's.
//     This is the blind spot batch 18 declared and batch 19 closed.
//
//  4. ROUND TRIP (CheckRoundTrip) — the READ side. Halves 1-3 gate the
//     agent → mirror direction only; a mirror → agent converter that drops a
//     field is invisible to all three, because the byte it dropped was written
//     correctly. Half 4 fills an agent value totally, runs the REAL write
//     converter and the REAL read converter back to back (through the actual
//     serialized bytes where the mirror has an on-disk form), and requires
//     WHOLE-STRUCT equality with what it started from. This is the shape
//     internal/lm/grpc's T7 total-struct proto parity already uses in both
//     directions, and the reason it caught five dropped fields a per-field
//     named assertion had missed: total equality of a fully-populated value is
//     the only assertion that can see an ABSENT STATEMENT.
//
// A FOURTH mirror exists — the runner↔plugin gRPC wire (internal/lm/grpc) —
// and is deliberately NOT a pair here: it already carries a stronger gate of
// its own (its T7 total-struct proto parity, a reflection-filled round trip
// requiring whole-struct equality in BOTH directions). Nothing is gained by
// pointing a weaker one-directional gate at it.
//
// Which mirror gets half 4 is decided by whether a read side EXISTS, not by
// whether anyone remembered to gate it: the `--format json` NDJSON DTOs have
// no in-repo mirror → agent converter at all (they are write-only wire), so
// there is nothing to round-trip; the transcript's history.go does have one
// (entriesFromRecord), and it is gated. If a read side is ever added to the
// DTOs, it inherits this half.
package parity

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Pair is one agent-type ↔ mirror-type projection, plus the converter that is
// the actual production code path between them.
type Pair struct {
	Name string
	// AgentType and MirrorType are zero values used only for reflection.
	AgentType  any
	MirrorType any
	// Exempt maps an agent field name that legitimately has NO mirror field to
	// the reason it has none. Adding an entry here is a deliberate act.
	Exempt map[string]string
	// Convert runs the production converter over a filled agent value
	// (addressable, of AgentType) and returns the mirror value it produced.
	Convert func(v reflect.Value) any
}

// CheckFieldCount is half 1: structural NumField parity, modulo Exempt.
func CheckFieldCount(t *testing.T, pairs []Pair) {
	t.Helper()
	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			at := reflect.TypeOf(p.AgentType)
			mt := reflect.TypeOf(p.MirrorType)
			// Exported fields only on BOTH sides: a protobuf-generated mirror
			// carries three unexported bookkeeping fields (state, sizeCache,
			// unknownFields) that are not part of the projection and would
			// otherwise mask three real drops.
			want := exportedFieldCount(at) - len(p.Exempt)
			if exportedFieldCount(mt) != want {
				t.Errorf("%s has %d exported fields; %s has %d (%d exempt) — expected %d mirror fields.\n"+
					"agent fields: %s\nmirror fields: %s\n"+
					"A field added to the agent type with no mirror is silent data loss on every byte this mirror writes. "+
					"Either mirror it (struct + converter + published schema) or add it to this pair's Exempt map with a reason.",
					at.Name(), exportedFieldCount(at), mt.Name(), exportedFieldCount(mt), len(p.Exempt), want,
					strings.Join(FieldNames(at), ", "), strings.Join(FieldNames(mt), ", "))
			}
			for name := range p.Exempt {
				if _, ok := at.FieldByName(name); !ok {
					t.Errorf("Exempt map names %s.%s, which does not exist — stale exemption", at.Name(), name)
				}
			}
		})
	}
}

// CheckConverterCopies is half 2: per-field sentinel byte survival.
func CheckConverterCopies(t *testing.T, pairs []Pair) {
	t.Helper()
	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			at := reflect.TypeOf(p.AgentType)
			filled := reflect.New(at).Elem()
			var bag sentinelBag
			fillSentinels(t, filled, at.Name(), p.Exempt, &bag)

			raw := marshalConverted(t, p, filled)
			leaves := collectLeaves(t, raw)

			for _, want := range bag.scalars {
				if !leaves.hasScalar(want.value) {
					t.Errorf("field %s did not survive the converter: no leaf carrying %v in the marshalled mirror.\n"+
						"mirror JSON: %s\n"+
						"A field the converter never copies is lost from every byte this mirror writes.",
						want.path, want.value, raw)
				}
			}
			if leaves.trues != bag.bools {
				t.Errorf("bool parity: filled %d bool field(s) (%s) with true but the marshalled mirror carries %d true leaf/leaves.\n"+
					"mirror JSON: %s",
					bag.bools, strings.Join(bag.boolPaths, ", "), leaves.trues, raw)
			}
		})
	}
}

// CheckBoolIsolation is half 3: each bool field, set true ALONE, must produce
// exactly one true leaf, at a key no other bool field also lands on.
//
// Half 2 counts trues, which a converter that writes one source bool into two
// mirror slots satisfies while dropping the other source bool entirely. Only
// setting each bool alone can tell those apart.
func CheckBoolIsolation(t *testing.T, pairs []Pair) {
	t.Helper()
	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			at := reflect.TypeOf(p.AgentType)
			paths := boolPaths(at, at.Name(), p.Exempt)
			if len(paths) == 0 {
				t.Skip("no bool fields")
			}
			landed := map[string]string{} // json key path -> agent field path
			for _, bp := range paths {
				v := reflect.New(at).Elem()
				if !buildOnly(v, at.Name(), bp) {
					t.Fatalf("could not construct a value with only %s set — teach buildOnly about its shape", bp)
				}
				raw := marshalConverted(t, p, v)
				keys := trueKeys(t, raw)
				if len(keys) != 1 {
					t.Errorf("bool isolation: with ONLY %s set true, the mirror carries %d true leaf/leaves %v, want exactly 1.\n"+
						"mirror JSON: %s\n"+
						"Zero means the converter never copies this bool (silent loss); more than one means it fans a single "+
						"source bool into several mirror slots, which the count-based half cannot see.",
						bp, len(keys), keys, raw)
					continue
				}
				if prev, dup := landed[keys[0]]; dup {
					t.Errorf("bool isolation: %s and %s BOTH land on mirror key %q — one of them is being written into the other's slot.\n"+
						"mirror JSON: %s", prev, bp, keys[0], raw)
					continue
				}
				landed[keys[0]] = bp
			}
		})
	}
}

// RoundTripPair is one agent type plus the production path that writes it to a
// mirror and reads it back. Unlike Pair it names no mirror type: half 4 does
// not care what shape the bytes took in between, only that nothing was lost
// crossing them twice.
type RoundTripPair struct {
	Name string
	// AgentType is a zero value of the type under gate, used for reflection.
	AgentType any
	// Exempt maps an agent field name that CANNOT survive the round trip to the
	// reason. An exempt field is left zero on the way in and must come back
	// zero — so an exemption weakens the gate to "this field is not carried",
	// which is still an assertion, rather than removing it from the comparison.
	// Adding an entry here is a deliberate act; "the reader forgot it" is a bug
	// to fix, never an entry to add.
	Exempt map[string]string
	// RoundTrip runs the REAL write converter and the REAL read converter over
	// a filled agent value (addressable, of AgentType) and returns what came
	// back, as a value of AgentType. Where the mirror has a serialized form,
	// RoundTrip must go through the actual bytes: a converter pair tested
	// struct-to-struct still passes when the two sides disagree about a JSON
	// tag.
	RoundTrip func(v reflect.Value) any
}

// CheckRoundTrip is half 4: fill totally, write, read back, require the whole
// struct.
//
// It asserts on TOTAL equality rather than on named fields deliberately. A
// per-field assertion covers the fields whoever wrote it thought of, and a
// field added tomorrow is silently uncovered — which is exactly how five
// dropped fields survived underneath internal/lm/grpc's previous round-trip
// test. Filling by reflection means a field added tomorrow is gated the day it
// lands, with nobody editing this file.
func CheckRoundTrip(t *testing.T, pairs []RoundTripPair) {
	t.Helper()
	for _, p := range pairs {
		t.Run(p.Name, func(t *testing.T) {
			at := reflect.TypeOf(p.AgentType)
			filled := reflect.New(at).Elem()
			var bag sentinelBag
			fillSentinels(t, filled, at.Name(), p.Exempt, &bag)

			for name := range p.Exempt {
				if _, ok := at.FieldByName(name); !ok {
					t.Errorf("Exempt map names %s.%s, which does not exist — stale exemption", at.Name(), name)
				}
			}

			back := p.RoundTrip(filled)
			gotV := reflect.ValueOf(back)
			if gotV.Type() != at {
				t.Fatalf("RoundTrip returned %s, want %s — half 4 compares whole structs and cannot do that across types", gotV.Type(), at)
			}

			diffs := fieldDiffs(filled, gotV, at.Name())
			if len(diffs) > 0 {
				t.Errorf("round trip lost or altered %d field(s) of %s:\n  %s\n"+
					"A field the READ converter never copies is unrecoverable from bytes that "+
					"were written correctly — the write-side halves of this gate cannot see it, "+
					"because nothing is wrong with what went to disk. Either restore it in the "+
					"read converter or add it to this pair's Exempt map with a reason.",
					len(diffs), at.Name(), strings.Join(diffs, "\n  "))
			}

			// BOOL ISOLATION on the read side. The total-fill pass above sets
			// EVERY bool true, which makes it blind to exactly the defect half 3
			// exists to catch on the write side: a reader assigning one source
			// bool into two destination slots (`Sidechain: e.IsError`) returns
			// true == true and the whole struct still matches. Verified, not
			// assumed — that mutation passed the total-fill pass green.
			//
			// Setting each bool ALONE tells them apart: with only IsError set,
			// a Sidechain that comes back true is a diff. Every other field
			// stays zero and must come back zero, so this also catches a reader
			// that invents a value it was never given.
			for _, bp := range boolPaths(at, at.Name(), p.Exempt) {
				v := reflect.New(at).Elem()
				if !buildOnly(v, at.Name(), bp) {
					t.Fatalf("could not construct a value with only %s set — teach buildOnly about its shape", bp)
				}
				isoBack := p.RoundTrip(v)
				isoGot := reflect.ValueOf(isoBack)
				if d := fieldDiffs(v, isoGot, at.Name()); len(d) > 0 {
					t.Errorf("round-trip bool isolation: with ONLY %s set true, %d field(s) came back wrong:\n  %s\n"+
						"A read converter that fans one source bool into several destination slots "+
						"(or invents a value it was never given) survives the whole-struct pass, "+
						"because that pass sets every bool true and true == true.",
						bp, len(d), strings.Join(d, "\n  "))
				}
			}
		})
	}
}

// fieldDiffs reports every leaf at which want and got disagree, by path.
// time.Time is compared by instant (a mirror that serializes and re-parses a
// timestamp legitimately rebuilds the struct), everything else by DeepEqual.
func fieldDiffs(want, got reflect.Value, path string) []string {
	if isTime(want.Type()) {
		w := want.Interface().(time.Time)
		g := got.Interface().(time.Time)
		if !w.Equal(g) {
			return []string{fmt.Sprintf("%s: wrote %s, read back %s", path, w.Format(time.RFC3339Nano), g.Format(time.RFC3339Nano))}
		}
		return nil
	}
	switch want.Kind() {
	case reflect.Struct:
		var out []string
		typ := want.Type()
		for i := 0; i < typ.NumField(); i++ {
			if typ.Field(i).PkgPath != "" {
				continue
			}
			out = append(out, fieldDiffs(want.Field(i), got.Field(i), path+"."+typ.Field(i).Name)...)
		}
		return out
	case reflect.Slice:
		if want.Type().Elem().Kind() == reflect.Uint8 { // json.RawMessage & friends
			break
		}
		if want.Len() != got.Len() {
			return []string{fmt.Sprintf("%s: wrote %d element(s), read back %d", path, want.Len(), got.Len())}
		}
		var out []string
		for i := 0; i < want.Len(); i++ {
			out = append(out, fieldDiffs(want.Index(i), got.Index(i), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	if !reflect.DeepEqual(want.Interface(), got.Interface()) {
		return []string{fmt.Sprintf("%s: wrote %#v, read back %#v", path, want.Interface(), got.Interface())}
	}
	return nil
}

func marshalConverted(t *testing.T, p Pair, filled reflect.Value) []byte {
	t.Helper()
	mirror := p.Convert(filled)
	raw, err := json.Marshal(mirror)
	if err != nil {
		t.Fatalf("marshal mirror: %v", err)
	}
	return raw
}

// --- sentinel machinery ----------------------------------------------------

// scalarSentinel is one field's unique stamp: the path it was written to and
// the single value that must show up on the other side.
type scalarSentinel struct {
	path  string
	value any // string or float64
}

type sentinelBag struct {
	scalars   []scalarSentinel
	bools     int
	boolPaths []string
	n         int
}

func (b *sentinelBag) next() int { b.n++; return b.n }

// sentinelEpoch is the base for time.Time sentinels: a whole second, so
// RFC3339 and RFC3339Nano render the same bytes and either encoding of a
// mirrored timestamp is found by the containment check.
var sentinelEpoch = time.Date(2033, 3, 3, 3, 3, 3, 0, time.UTC)

// fillSentinels populates every exported field of v (recursively) with a value
// unique to that field's path, recording what it wrote in bag. Exempt applies
// to TOP-LEVEL field names only — a nested type's fields are never exempt,
// because a nested mirror struct has no envelope to hoist anything into.
func fillSentinels(t *testing.T, v reflect.Value, path string, exempt map[string]string, bag *sentinelBag) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		if _, skip := exempt[f.Name]; skip && !strings.Contains(path, ".") {
			continue
		}
		fillValue(t, v.Field(i), path+"."+f.Name, bag)
	}
}

func fillValue(t *testing.T, fv reflect.Value, path string, bag *sentinelBag) {
	t.Helper()
	switch fv.Kind() {
	case reflect.String:
		s := fmt.Sprintf("sentinel-%d-%s", bag.next(), lastSegment(path))
		fv.SetString(s)
		bag.scalars = append(bag.scalars, scalarSentinel{path: path, value: s})
	case reflect.Bool:
		fv.SetBool(true)
		bag.bools++
		bag.boolPaths = append(bag.boolPaths, path)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := int64(700000 + bag.next())
		fv.SetInt(n)
		bag.scalars = append(bag.scalars, scalarSentinel{path: path, value: float64(n)})
	case reflect.Float32, reflect.Float64:
		f := 900000 + float64(bag.next())
		fv.SetFloat(f)
		bag.scalars = append(bag.scalars, scalarSentinel{path: path, value: f})
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.Uint8 { // json.RawMessage & friends
			s := fmt.Sprintf("sentinel-%d-%s", bag.next(), lastSegment(path))
			fv.SetBytes([]byte(`{"parity":"` + s + `"}`))
			bag.scalars = append(bag.scalars, scalarSentinel{path: path, value: s})
			return
		}
		elem := reflect.New(fv.Type().Elem()).Elem()
		fillValue(t, elem, path+"[0]", bag)
		fv.Set(reflect.Append(fv, elem))
	case reflect.Struct:
		if isTime(fv.Type()) {
			ts := sentinelEpoch.Add(time.Duration(bag.next()) * time.Second)
			fv.Set(reflect.ValueOf(ts))
			bag.scalars = append(bag.scalars, scalarSentinel{path: path, value: ts.Format(time.RFC3339)})
			return
		}
		// Other opaque structs must be exempted explicitly; blindly filling
		// their unexported state is meaningless.
		if exportedFieldCount(fv.Type()) == 0 {
			t.Fatalf("parity gate: %s is an opaque struct (%s) with no exported fields — "+
				"add it to the pair's Exempt map with a reason, or teach fillValue about it", path, fv.Type())
		}
		fillSentinels(t, fv, path, nil, bag)
	default:
		t.Fatalf("parity gate: field %s has unsupported kind %s (%s) — teach fillValue about it "+
			"rather than letting the gate silently stop covering this field", path, fv.Kind(), fv.Type())
	}
}

func isTime(t reflect.Type) bool { return t == reflect.TypeOf(time.Time{}) }

func exportedFieldCount(t reflect.Type) int {
	n := 0
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath == "" {
			n++
		}
	}
	return n
}

// --- bool-isolation machinery ---------------------------------------------

// boolPaths enumerates every reachable bool field path under typ, using the
// same [0] slice-element notation fillSentinels writes. Exempt applies to
// top-level names only, exactly as in half 2.
func boolPaths(typ reflect.Type, path string, exempt map[string]string) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if _, skip := exempt[f.Name]; skip && !strings.Contains(path, ".") {
			continue
		}
		out = append(out, boolPathsOf(f.Type, path+"."+f.Name)...)
	}
	return out
}

func boolPathsOf(t reflect.Type, path string) []string {
	switch t.Kind() {
	case reflect.Bool:
		return []string{path}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil
		}
		return boolPathsOf(t.Elem(), path+"[0]")
	case reflect.Struct:
		if isTime(t) {
			return nil
		}
		return boolPaths(t, path, nil)
	default:
		return nil
	}
}

// buildOnly sets exactly the bool at target true within v, allocating whatever
// slice elements the path passes through and leaving every other field zero.
func buildOnly(v reflect.Value, path, target string) bool {
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		fp := path + "." + f.Name
		if !strings.HasPrefix(target, fp) {
			continue
		}
		if buildOnlyValue(v.Field(i), fp, target) {
			return true
		}
	}
	return false
}

func buildOnlyValue(fv reflect.Value, path, target string) bool {
	switch fv.Kind() {
	case reflect.Bool:
		if path == target {
			fv.SetBool(true)
			return true
		}
	case reflect.Slice:
		if fv.Type().Elem().Kind() == reflect.Uint8 {
			return false
		}
		elem := reflect.New(fv.Type().Elem()).Elem()
		if !buildOnlyValue(elem, path+"[0]", target) {
			return false
		}
		fv.Set(reflect.Append(fv, elem))
		return true
	case reflect.Struct:
		if isTime(fv.Type()) {
			return false
		}
		return buildOnly(fv, path, target)
	}
	return false
}

// trueKeys returns the JSON key path of every `true` leaf in raw, sorted.
func trueKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal mirror: %v", err)
	}
	var out []string
	var walk func(string, any)
	walk = func(prefix string, x any) {
		switch tv := x.(type) {
		case map[string]any:
			for k, val := range tv {
				walk(prefix+"."+k, val)
			}
		case []any:
			for _, val := range tv {
				walk(prefix+"[]", val)
			}
		case bool:
			if tv {
				out = append(out, strings.TrimPrefix(prefix, "."))
			}
		}
	}
	walk("", decoded)
	sort.Strings(out)
	return out
}

// --- leaf collection -------------------------------------------------------

type leafSet struct {
	strs  []string
	nums  []float64
	trues int
}

func (l leafSet) hasScalar(want any) bool {
	switch w := want.(type) {
	case string:
		for _, s := range l.strs {
			// A string sentinel may arrive verbatim (a copied string field) or
			// embedded in a JSON blob (a json.RawMessage field), so a
			// containment check is the honest one for both.
			if s == w || strings.Contains(s, w) {
				return true
			}
		}
	case float64:
		for _, n := range l.nums {
			if n == w {
				return true
			}
		}
	}
	return false
}

func collectLeaves(t *testing.T, raw []byte) leafSet {
	t.Helper()
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal mirror: %v", err)
	}
	var out leafSet
	var walk func(any)
	walk = func(x any) {
		switch tv := x.(type) {
		case map[string]any:
			for _, val := range tv {
				walk(val)
			}
		case []any:
			for _, val := range tv {
				walk(val)
			}
		case string:
			out.strs = append(out.strs, tv)
		case float64:
			out.nums = append(out.nums, tv)
		case bool:
			if tv {
				out.trues++
			}
		}
	}
	walk(decoded)
	return out
}

// FieldNames lists a struct type's EXPORTED field names, for failure messages
// — matching what exportedFieldCount counts, so the message and the count
// never disagree.
func FieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath == "" {
			out = append(out, t.Field(i).Name)
		}
	}
	return out
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

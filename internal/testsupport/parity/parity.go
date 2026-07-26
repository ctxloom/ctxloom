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
// The remaining declared blind spot is the READ side: these halves gate the
// agent → mirror direction only. A mirror → agent converter that drops a field
// is not covered here (the `--format json` DTOs have no in-repo read side at
// all; the transcript's history.go does).
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
			want := at.NumField() - len(p.Exempt)
			if mt.NumField() != want {
				t.Errorf("%s has %d fields; %s has %d (%d exempt) — expected %d mirror fields.\n"+
					"agent fields: %s\nmirror fields: %s\n"+
					"A field added to the agent type with no mirror is silent data loss on every byte this mirror writes. "+
					"Either mirror it (struct + converter + published schema) or add it to this pair's Exempt map with a reason.",
					at.Name(), at.NumField(), mt.Name(), mt.NumField(), len(p.Exempt), want,
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
		if fv.NumField() == 0 || !hasExportedFields(fv.Type()) {
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

func hasExportedFields(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath == "" {
			return true
		}
	}
	return false
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

// FieldNames lists a struct type's field names, for failure messages.
func FieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

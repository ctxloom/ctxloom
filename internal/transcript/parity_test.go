package transcript

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ---------------------------------------------------------------------------
// U144-F10 — the class gate behind U144-F01 and U144-F02.
//
// record.go:9-12 claims the on-disk payload structs mirror agent.ChatEvent's
// variants "field-for-field". Nothing enforced that claim, and it had already
// silently failed TWICE (ChatSessionInfo.Resumable, PermissionRequest.
// ToolCallID) before anyone noticed. Each new agent field must today be
// edited in four places (agent type → payload struct → to-payload converter →
// from-payload converter); this gate turns "someone forgot one of the four"
// from a silent on-disk data loss into a build failure.
//
// The gate has two halves, deliberately different in kind so that neither can
// be satisfied by the wrong evidence:
//
//  1. STRUCTURAL — reflect.NumField parity between the agent type and its
//     payload mirror, modulo an explicit, reasoned exemption list. Catches a
//     field added to the agent type with no mirror at all.
//
//  2. BYTE SURVIVAL — every field of a fully-populated agent value is stamped
//     with a value unique to that field, the REAL converter is run, the result
//     is marshalled, and every sentinel value must be found among the leaves
//     of the resulting JSON. Catches a field that HAS a mirror but that the
//     converter never copies — the exact shape of F01/F02, which a name-level
//     or field-count check alone would have passed the moment someone added
//     the struct field without touching the converter.
//
// Half 2 asserts on surviving VALUES, not on names: a converter that copies
// field A into field B's slot still fails, because each field's sentinel is
// distinct. Booleans cannot carry a distinct token, so they are gated by
// COUNT (n bool fields filled true ⇒ n `true` leaves must survive).
// ---------------------------------------------------------------------------

// parityPair is one agent-type ↔ payload-type mirror, plus the converter that
// is the actual production code path between them.
type parityPair struct {
	name string
	// agentType and payloadType are zero values used only for reflection.
	agentType   any
	payloadType any
	// exempt maps an agent field name that legitimately has NO payload mirror
	// to the reason it has none. Adding an entry here is a deliberate act.
	exempt map[string]string
	// convert runs the production converter over a filled agent value
	// (addressable, of agentType) and returns the payload it produced.
	convert func(v reflect.Value) any
}

func parityPairs() []parityPair {
	return []parityPair{
		{
			name:        "SessionEntry↔EntryPayload",
			agentType:   agent.SessionEntry{},
			payloadType: EntryPayload{},
			exempt: map[string]string{
				"Timestamp": "hoisted to the envelope's Record.TS",
			},
			convert: func(v reflect.Value) any {
				e := v.Addr().Interface().(*agent.SessionEntry)
				return entryPayload(e)
			},
		},
		{
			name:        "ChatSessionInfo↔SessionPayload",
			agentType:   agent.ChatSessionInfo{},
			payloadType: SessionPayload{},
			exempt: map[string]string{
				"SessionID": "hoisted to the envelope's Record.SessionID",
			},
			convert: func(v reflect.Value) any {
				s := v.Addr().Interface().(*agent.ChatSessionInfo)
				return sessionPayload(s)
			},
		},
		{
			name:        "TurnMeta↔CompletePayload",
			agentType:   agent.TurnMeta{},
			payloadType: CompletePayload{},
			exempt:      map[string]string{},
			convert: func(v reflect.Value) any {
				m := v.Addr().Interface().(*agent.TurnMeta)
				return completePayload(m)
			},
		},
		{
			name:        "PermissionRequest↔PermissionPayload",
			agentType:   agent.PermissionRequest{},
			payloadType: PermissionPayload{},
			exempt:      map[string]string{},
			convert: func(v reflect.Value) any {
				p := v.Addr().Interface().(*agent.PermissionRequest)
				return permissionPayload(p)
			},
		},
	}
}

func TestPayloadMirrorsAgentTypeFieldCount(t *testing.T) {
	for _, p := range parityPairs() {
		t.Run(p.name, func(t *testing.T) {
			at := reflect.TypeOf(p.agentType)
			pt := reflect.TypeOf(p.payloadType)
			want := at.NumField() - len(p.exempt)
			if pt.NumField() != want {
				t.Errorf("%s has %d fields; %s has %d (%d exempt) — expected %d payload fields.\n"+
					"agent fields: %s\npayload fields: %s\n"+
					"A field added to the agent type with no on-disk mirror is silent data loss in every canonical transcript (U144-F01/F02). "+
					"Either mirror it (struct + converter + docs/transcript.schema.json) or add it to this pair's exempt map with a reason.",
					at.Name(), at.NumField(), pt.Name(), pt.NumField(), len(p.exempt), want,
					strings.Join(fieldNames(at), ", "), strings.Join(fieldNames(pt), ", "))
			}
			for name := range p.exempt {
				if _, ok := at.FieldByName(name); !ok {
					t.Errorf("exempt map names %s.%s, which does not exist — stale exemption", at.Name(), name)
				}
			}
		})
	}
}

func TestPayloadConverterCopiesEveryAgentField(t *testing.T) {
	for _, p := range parityPairs() {
		t.Run(p.name, func(t *testing.T) {
			at := reflect.TypeOf(p.agentType)
			filled := reflect.New(at).Elem()
			var bag sentinelBag
			fillSentinels(t, filled, at.Name(), p.exempt, &bag)

			payload := p.convert(filled)
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			leaves := collectLeaves(decoded)

			for _, want := range bag.scalars {
				if !leaves.hasScalar(want.value) {
					t.Errorf("field %s did not survive the converter: no leaf carrying %v in the marshalled payload.\n"+
						"payload JSON: %s\n"+
						"A field the converter never copies is lost from every canonical transcript on disk (U144-F01/F02).",
						want.path, want.value, raw)
				}
			}
			if leaves.trues != bag.bools {
				t.Errorf("bool parity: filled %d bool field(s) (%s) with true but the marshalled payload carries %d true leaf/leaves.\n"+
					"payload JSON: %s",
					bag.bools, strings.Join(bag.boolPaths, ", "), leaves.trues, raw)
			}
		})
	}
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
		// time.Time and other opaque structs must be exempted explicitly;
		// blindly filling their unexported state is meaningless.
		if fv.NumField() == 0 || !hasExportedFields(fv.Type()) {
			t.Fatalf("parity gate: %s is an opaque struct (%s) with no exported fields — "+
				"add it to the pair's exempt map with a reason, or teach fillValue about it", path, fv.Type())
		}
		fillSentinels(t, fv, path, nil, bag)
	default:
		t.Fatalf("parity gate: field %s has unsupported kind %s (%s) — teach fillValue about it "+
			"rather than letting the gate silently stop covering this field", path, fv.Kind(), fv.Type())
	}
}

func hasExportedFields(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).PkgPath == "" {
			return true
		}
	}
	return false
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

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

func collectLeaves(v any) leafSet {
	var out leafSet
	var walk func(any)
	walk = func(x any) {
		switch t := x.(type) {
		case map[string]any:
			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, val := range t {
				walk(val)
			}
		case string:
			out.strs = append(out.strs, t)
		case float64:
			out.nums = append(out.nums, t)
		case bool:
			if t {
				out.trues++
			}
		}
	}
	walk(v)
	return out
}

func fieldNames(t reflect.Type) []string {
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}

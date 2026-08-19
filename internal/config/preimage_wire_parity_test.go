//go:build arch

package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport/parity"
)

// ---------------------------------------------------------------------------
// SIGNED-PREIMAGE <-> WIRE PARITY
//
// A bundle's executable items (hooks, MCP servers) are approved by hashing a
// PREIMAGE — the canonical JSON that BundleHook.ContentPayload /
// BundleMCP.ContentPayload emit — and are DELIVERED as wire.Hook /
// wire.MCPServer. Those are two different field sets, written in two different
// files, by two different hands. When a field is in the preimage but never
// crosses into the wire type, the artifact that runs is NOT the artifact the
// signature covered, and nothing complains: both sides hash successfully and
// simply disagree about what was approved. That is exactly what happened to
// Hook.PreToolFallback.
//
// This gate is a DIFFERENT AXIS from the two that already exist and it is not
// reachable from either:
//
//   - internal/lm/grpc's total-struct proto parity gate couples the Go WIRE
//     type to its PROTO mirror, in both directions. It starts one step
//     downstream of here: it can prove wire.Hook.PreToolFallback reaches the
//     proto, and says nothing about whether the signed preimage reached
//     wire.Hook.
//   - internal/testsupport/parity couples agent.ChatEvent to its transcript and
//     `--format json` mirrors. Different types entirely.
//
// This gate and the proto parity gate compose into the whole chain: preimage
// → wire (here) → proto (the proto parity gate).
//
// WHAT COUNTS AS EVIDENCE, and why nothing weaker will do
//
// The suggested remedy for the PreToolFallback gap was to "reflect over
// hookContentPayload / mcpContentPayload field names and assert each has a counterpart on
// wire.Hook / wire.MCPServer". A name-scan is NOT enough, and this programme
// has the scar tissue to prove it: a previous gate scanned names, passed, and
// its finding stayed open — because a wire type can carry a field of the right
// name that no converter ever writes. A matching name is a claim; a surviving
// byte is a fact. So this gate asserts on bytes, from the real code path, at
// both ends:
//
//  1. THE SIGNED SET IS TAKEN FROM THE PREIMAGE THE PRODUCTION PATH ACTUALLY
//     HASHED. extractHooksFromBundle / extractMCPFromBundle build the preimage
//     themselves and hand it to the trust gate; the test installs a capturing
//     gate and reads the field set off THOSE bytes. It never reflects over
//     hookContentPayload's Go field names — a field added to the preimage
//     struct shows up here the moment ContentPayload emits it, and there is no
//     second list to keep in sync.
//
//  2. EACH SIGNED FIELD MUST SURVIVE AS A DISTINCT VALUE. Every field of the
//     bundle item is stamped with a sentinel unique to that field, the REAL
//     converter runs, and the resulting wire value is marshalled. A signed
//     field's sentinel must appear among the wire leaves. Because the sentinels
//     are distinct, a converter that copies Matcher into the Command slot still
//     fails — presence of *a* value is not accepted, only presence of *that*
//     value.
//
//  3. BOOLS ARE NEVER GATED BY COUNT. A bool cannot carry a unique sentinel:
//     `true` is `true`. A gate that counts trues is satisfiable by the wrong
//     evidence (one source bool fanned into two wire slots counts the same as
//     two carried bools), and this programme already shipped one that passed a
//     swap. So every bool signed field is set true ALONE — every other field on
//     the item left zero — and the wire value must carry EXACTLY ONE true leaf,
//     at a JSON key no other bool signed field also lands on. PreToolFallback,
//     the field the original finding was about, is a bool; this is the half
//     that actually covers it.
//
//  4. WHICH SIGNED KEYS ARE BOOLS IS ALSO DISCOVERED, NOT DECLARED. The map
//     from preimage key to Go field is built by setting each bool field true
//     alone and observing which preimage key flips — so a rename cannot
//     decouple the two halves, and a preimage builder that fanned one Go bool
//     into two signed keys fails here rather than silently splitting coverage.
//
// WHAT THIS GATE CANNOT REACH
//
//   - It gates the WRITE direction only (preimage → wire). There is no
//     wire → bundle-item reader in the repo, so there is nothing to round-trip;
//     if one is ever added it needs its own half.
//   - It stops at wire.Hook / wire.MCPServer. Everything downstream — the proto
//     mirror, and each backend's settings writer — is somebody else's gate
//     (T7 for the proto).
//   - It cannot prove a signed field is SEMANTICALLY honoured by the engine,
//     only that its value crossed the seam intact.
//   - Preimage keys whose value is a JSON null or an empty container carry no
//     distinguishable evidence; rather than skip them the gate FAILS LOUDLY and
//     asks to be taught, because a silently uncovered field is the defect this
//     file exists to prevent.
// ---------------------------------------------------------------------------

// preimageWireExemptions names every preimage key that deliberately does NOT
// have to reach the wire type, keyed "<subject>.<preimage key>" with the reason.
// This is the only escape hatch, and TestPreimageWireExemptionsAreLive fails if
// an entry stops matching a real preimage key — so a rename cannot leave a stale
// exemption behind and silently un-cover whatever inherits the name.
//
// A key belongs here only when it is STRUCTURALLY not a property of the item.
// "The converter forgot it" is a bug to fix, never an entry to add.
var preimageWireExemptions = map[string]string{
	// signing.ExecPreimageContract: the version of the PREIMAGE FORMAT, not a
	// property of the hook. It exists so a change to the signed field set can
	// invalidate old approvals (spec §3.3.2); the wire type describes what to
	// run, and a hook does not "have" a contract version to deliver.
	"hook.preimage": "preimage-format version (signing.ExecPreimageContract), not a field of the item; nothing downstream consumes it",
	"mcp.preimage":  "preimage-format version (signing.ExecPreimageContract), not a field of the item; nothing downstream consumes it",
}

// preimageSubject is one signed bundle item plus the REAL production path that
// turns it into wire types. Deliver returns the preimage bytes that path itself
// fed to the trust gate — not a preimage the test built — so the two ends being
// compared provably come from the same value in the same code path.
type preimageSubject struct {
	Name     string
	ItemType reflect.Type
	WireType reflect.Type
	// Deliver runs the production converter over a filled item and returns the
	// captured preimage plus each marshalled wire value it produced, keyed by a
	// label for failure messages (the six hook events; the single MCP server).
	Deliver func(t *testing.T, item reflect.Value) (preimage []byte, wireJSON map[string][]byte)
}

func preimageSubjects() []preimageSubject {
	return []preimageSubject{
		{
			Name:     "hook",
			ItemType: reflect.TypeOf(bundles.BundleHook{}),
			WireType: reflect.TypeOf(wire.Hook{}),
			Deliver:  deliverHookToWire,
		},
		{
			Name:     "mcp",
			ItemType: reflect.TypeOf(bundles.BundleMCP{}),
			WireType: reflect.TypeOf(wire.MCPServer{}),
			Deliver:  deliverMCPToWire,
		},
	}
}

// deliverHookToWire puts the item in all six hook events and runs
// extractHooksFromBundle — the production path — with a capturing always-allow
// gate. Using every event is not redundancy: the six slots are wired by hand,
// and a slot that dropped the hook would otherwise be invisible here.
func deliverHookToWire(t *testing.T, item reflect.Value) ([]byte, map[string][]byte) {
	t.Helper()
	h, ok := item.Interface().(bundles.BundleHook)
	require.True(t, ok, "subject item is not a bundles.BundleHook")

	one := func() []bundles.BundleHook { return []bundles.BundleHook{h} }
	bundle := &bundles.Bundle{Hooks: bundles.BundleHooks{
		PreTool:      one(),
		PostTool:     one(),
		SessionStart: one(),
		SessionEnd:   one(),
		PreShell:     one(),
		PostFileEdit: one(),
	}}

	var payloads [][]byte
	gate := bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		payloads = append(payloads, append([]byte(nil), e.Bytes...))
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
	})

	got := extractHooksFromBundle(bundles.ProjectAuthoredRead("fixture", bundle), mustLocalRef(t, "parity-src"), gate)

	out := map[string][]byte{}
	for label, hooks := range map[string][]wire.Hook{
		"pre_tool":       got.PreTool,
		"post_tool":      got.PostTool,
		"session_start":  got.SessionStart,
		"session_end":    got.SessionEnd,
		"pre_shell":      got.PreShell,
		"post_file_edit": got.PostFileEdit,
	} {
		if len(hooks) != 1 {
			t.Fatalf("event %s produced %d wire hooks, want 1 — the production path dropped the hook before any field could be checked", label, len(hooks))
		}
		raw, err := json.Marshal(hooks[0])
		require.NoErrorf(t, err, "marshal wire hook for event %s", label)
		out[label] = raw
	}

	require.NotEmpty(t, payloads, "the trust gate was never called — no preimage was built, so there is nothing to compare against")
	for i, p := range payloads[1:] {
		require.Equalf(t, string(payloads[0]), string(p),
			"event %d hashed a DIFFERENT preimage for the same hook — the signed bytes must not depend on which event a hook is wired to", i+1)
	}
	return payloads[0], out
}

// deliverMCPToWire runs extractMCPFromBundle — the production path — with a
// capturing always-allow gate.
func deliverMCPToWire(t *testing.T, item reflect.Value) ([]byte, map[string][]byte) {
	t.Helper()
	m, ok := item.Interface().(bundles.BundleMCP)
	require.True(t, ok, "subject item is not a bundles.BundleMCP")

	bundle := &bundles.Bundle{MCP: map[string]bundles.BundleMCP{"parity": m}}

	var payload []byte
	gate := bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		payload = append([]byte(nil), e.Bytes...)
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
	})

	servers := extractMCPFromBundle(bundles.ProjectAuthoredRead("fixture", bundle), mustLocalRef(t, "parity-src"), gate)
	srv, ok := servers["parity"]
	if !ok {
		t.Fatal("the production path produced no wire MCP server — nothing to compare against")
	}
	raw, err := json.Marshal(srv)
	require.NoError(t, err, "marshal wire MCP server")

	require.NotEmpty(t, payload, "the trust gate was never called — no preimage was built")
	return payload, map[string][]byte{"server": raw}
}

// TestPreimageFieldsReachTheWire is halves 1+2: every non-bool signed field
// must arrive on the wire carrying ITS OWN value.
func TestArch_Preimage_SignedFieldsReachTheWire(t *testing.T) {
	for _, s := range preimageSubjects() {
		t.Run(s.Name, func(t *testing.T) {
			filled := reflect.New(s.ItemType).Elem()
			stampSentinels(t, filled, s.ItemType.Name())

			preimage, wires := s.Deliver(t, filled)
			signed := signedKeys(t, preimage)
			boolKeys := boolSignedKeys(t, s)

			checked := 0
			for _, key := range collections.SortedKeys(signed) {
				if reason, exempt := preimageWireExemptions[s.Name+"."+key]; exempt {
					t.Logf("preimage key %q exempt: %s", key, reason)
					continue
				}
				scalars, bools := leaves(t, signed[key])
				if bools > 0 {
					if _, known := boolKeys[key]; !known {
						t.Fatalf("preimage key %q carries a bool leaf but no single bool field of %s drives it — "+
							"this gate cannot produce distinguishing evidence for it. Teach boolSignedKeys about its shape "+
							"rather than letting it go silently uncovered.", key, s.ItemType.Name())
					}
					// Covered by TestPreimageBoolFieldsReachDistinctWireSlots:
					// a bool cannot carry a unique sentinel, and counting trues
					// is satisfiable by the wrong evidence.
					continue
				}
				if len(scalars) == 0 {
					t.Fatalf("preimage key %q produced no distinguishable leaf under the sentinel fill (raw: %s) — "+
						"an empty or null signed field is uncovered evidence. Teach stampSentinels to populate it.",
						key, signed[key])
				}
				checked++
				for label, raw := range wires {
					hay := wireLeaves(t, raw)
					for _, want := range scalars {
						if !hay[want] {
							t.Errorf("signed field %q did not reach the wire (%s, %s): its value %q is absent from the delivered %s.\n"+
								"preimage: %s\nwire:     %s\nwire fields: %s\n"+
								"A signed field that never crosses means the delivered artifact is NOT the artifact the "+
								"signature covered — and it is silent, because both sides hash successfully and simply disagree. "+
								"Carry it on %s (struct + converter), or add %q to preimageWireExemptions with a reason.",
								key, s.Name, label, want, s.WireType.Name(),
								preimage, raw, strings.Join(parity.FieldNames(s.WireType), ", "),
								s.WireType.Name(), s.Name+"."+key)
						}
					}
				}
			}

			require.NotZerof(t, checked+len(boolKeys),
				"no signed field was checked for subject %s — the gate is vacuous", s.Name)
		})
	}
}

// TestPreimageBoolFieldsReachDistinctWireSlots is half 3: each bool signed
// field, set true ALONE, must produce exactly one true leaf on the wire, at a
// key no other bool signed field also lands on.
//
// Counting trues under a total fill is NOT enough: a converter that writes one
// source bool into two wire slots while dropping another emits the same number
// of trues. Only isolation tells those apart.
func TestArch_Preimage_BoolFieldsReachDistinctWireSlots(t *testing.T) {
	for _, s := range preimageSubjects() {
		t.Run(s.Name, func(t *testing.T) {
			boolKeys := boolSignedKeys(t, s)
			if len(boolKeys) == 0 {
				t.Skipf("%s has no bool signed field", s.ItemType.Name())
			}
			// label -> wire JSON key -> the preimage key that landed there.
			landed := map[string]map[string]string{}
			for _, key := range collections.SortedKeys(boolKeys) {
				if _, exempt := preimageWireExemptions[s.Name+"."+key]; exempt {
					continue
				}
				field := boolKeys[key]
				v := reflect.New(s.ItemType).Elem()
				v.FieldByName(field).SetBool(true)

				_, wires := s.Deliver(t, v)
				for label, raw := range wires {
					got := parity.TrueKeys(t, raw)
					if len(got) != 1 {
						t.Errorf("bool isolation (%s, %s): with ONLY %s.%s set true, the delivered %s carries %d true leaf/leaves %v, want exactly 1.\n"+
							"wire: %s\n"+
							"Zero means the signed flag never reaches the wire — the delivered artifact is not the one signed. "+
							"More than one means a single signed bool is fanned into several wire slots, which a count-based check cannot see.",
							s.Name, label, s.ItemType.Name(), field, s.WireType.Name(), len(got), got, raw)
						continue
					}
					if landed[label] == nil {
						landed[label] = map[string]string{}
					}
					if prev, dup := landed[label][got[0]]; dup {
						t.Errorf("bool isolation (%s, %s): signed keys %q and %q BOTH land on wire key %q — one is being written into the other's slot.",
							s.Name, label, prev, key, got[0])
						continue
					}
					landed[label][got[0]] = key
				}
			}
		})
	}
}

// TestPreimageWireExemptionsAreLive is the companion to the exemption map: an
// entry that no longer matches a real preimage key is a stale exemption, and a
// stale exemption silently un-covers whatever field inherits the name.
func TestArch_Preimage_ExemptionsAreLive(t *testing.T) {
	live := map[string]bool{}
	for _, s := range preimageSubjects() {
		filled := reflect.New(s.ItemType).Elem()
		stampSentinels(t, filled, s.ItemType.Name())
		preimage, _ := s.Deliver(t, filled)
		for key := range signedKeys(t, preimage) {
			live[s.Name+"."+key] = true
		}
	}
	require.NotEmpty(t, live, "no preimage keys were discovered at all — the gate is not reaching the production path")

	for key, reason := range preimageWireExemptions {
		if !live[key] {
			t.Errorf("stale exemption %q (%q): no preimage emits that key any more. "+
				"Delete it, or repoint it — leaving it behind means whatever field inherits the name is exempt by accident.", key, reason)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("exemption %q has no written reason; an unexplained exemption is indistinguishable from a forgotten bug", key)
		}
	}
}

// --- evidence machinery ----------------------------------------------------

// signedKeys reads the signed field set off the preimage BYTES the production
// path hashed. Deliberately not reflection over hookContentPayload: the bytes
// are what the signature covers, and there is no second list to drift.
func signedKeys(t *testing.T, preimage []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	require.NoErrorf(t, json.Unmarshal(preimage, &doc), "preimage is not a JSON object: %s", preimage)
	require.NotEmptyf(t, doc, "preimage carries no fields at all: %s", preimage)
	return doc
}

// boolSignedKeys maps each preimage key whose value is a bool to the single
// exported Go field that drives it, DISCOVERED by setting each bool field true
// alone and observing which key flips. Nothing here is declared by name, so a
// rename cannot decouple this map from the field it describes.
func boolSignedKeys(t *testing.T, s preimageSubject) map[string]string {
	t.Helper()
	out := map[string]string{}
	for i := 0; i < s.ItemType.NumField(); i++ {
		f := s.ItemType.Field(i)
		if f.PkgPath != "" || f.Type.Kind() != reflect.Bool {
			continue
		}
		v := reflect.New(s.ItemType).Elem()
		v.FieldByName(f.Name).SetBool(true)
		preimage, _ := s.Deliver(t, v)
		flipped := parity.TrueKeys(t, preimage)
		switch len(flipped) {
		case 0:
			continue // not a signed field; nothing for this gate to require
		case 1:
			if prev, dup := out[flipped[0]]; dup {
				t.Fatalf("preimage key %q is driven by BOTH %s and %s — the preimage builder fans two fields into one signed slot",
					flipped[0], prev, f.Name)
			}
			out[flipped[0]] = f.Name
		default:
			t.Fatalf("%s.%s alone flips %d preimage keys %v — the preimage builder fans one field into several signed slots",
				s.ItemType.Name(), f.Name, len(flipped), flipped)
		}
	}
	return out
}

// stampSentinels fills every exported field of v with a value unique to that
// field, so a converter that copies field A into field B's slot is a failure
// rather than a pass. It fails loudly on any kind it cannot reach: a field the
// filler silently left zero is a field this gate stopped covering.
func stampSentinels(t *testing.T, v reflect.Value, path string) {
	t.Helper()
	n := 0
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		n++
		stampValue(t, v.Field(i), fmt.Sprintf("%s.%s", path, f.Name), n)
	}
}

func stampValue(t *testing.T, fv reflect.Value, path string, n int) {
	t.Helper()
	tok := func(suffix string) string {
		return fmt.Sprintf("sentinel-%d-%s%s", n, lastSegment(path), suffix)
	}
	switch fv.Kind() {
	case reflect.Pointer:
		// A pointer field is nil by default, and nil stamps as nothing — which
		// would leave the field silently uncovered rather than loudly unstamped.
		// Allocate and stamp the pointee, so an optional field is exercised
		// exactly as hard as a required one.
		fv.Set(reflect.New(fv.Type().Elem()))
		stampValue(t, fv.Elem(), path, n)
	case reflect.String:
		fv.SetString(tok(""))
	case reflect.Bool:
		fv.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		fv.SetInt(int64(770000 + n))
	case reflect.Slice:
		elem := reflect.New(fv.Type().Elem()).Elem()
		if elem.Kind() != reflect.String {
			t.Fatalf("preimage gate: %s is a []%s the sentinel filler cannot stamp — teach stampValue about it "+
				"rather than letting the gate silently stop covering this field", path, elem.Type())
		}
		elem.SetString(tok("-elem"))
		fv.Set(reflect.Append(fv, elem))
	case reflect.Map:
		if fv.Type().Key().Kind() != reflect.String || fv.Type().Elem().Kind() != reflect.String {
			t.Fatalf("preimage gate: %s is a %s the sentinel filler cannot stamp — teach stampValue about it", path, fv.Type())
		}
		fv.Set(reflect.MakeMap(fv.Type()))
		fv.SetMapIndex(reflect.ValueOf(tok("-key")), reflect.ValueOf(tok("-val")))
	default:
		t.Fatalf("preimage gate: field %s has unsupported kind %s (%s) — teach stampValue about it "+
			"rather than letting the gate silently stop covering this field", path, fv.Kind(), fv.Type())
	}
}

// leaves returns every distinguishable scalar under raw, plus how many bool
// leaves it carries. Map KEYS count as leaves because in a signed value's
// subtree (env) the keys are data, not schema.
func leaves(t *testing.T, raw []byte) ([]string, int) {
	t.Helper()
	var decoded any
	require.NoErrorf(t, json.Unmarshal(raw, &decoded), "unmarshal preimage value %s", raw)
	var out []string
	bools := 0
	var walk func(any)
	walk = func(x any) {
		switch tv := x.(type) {
		case map[string]any:
			for k, v := range tv {
				out = append(out, k)
				walk(v)
			}
		case []any:
			for _, v := range tv {
				walk(v)
			}
		case string:
			out = append(out, tv)
		case float64:
			out = append(out, fmt.Sprintf("%v", tv))
		case bool:
			bools++
		}
	}
	walk(decoded)
	sort.Strings(out)
	return out, bools
}

// wireLeaves is the haystack side: every string/number leaf AND every object
// key of the delivered wire value. Only sentinel-shaped values are ever looked
// up in it, so including schema keys costs nothing and lets a signed map key
// (env) be found where it lands.
func wireLeaves(t *testing.T, raw []byte) map[string]bool {
	t.Helper()
	vals, _ := leaves(t, raw)
	out := make(map[string]bool, len(vals))
	for _, v := range vals {
		out[v] = true
	}
	return out
}

func lastSegment(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

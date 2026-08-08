//go:build arch

package grpc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// T7 — TOTAL-STRUCT PROTO PARITY
//
// Every converter in this package is hand-written: a Go struct is copied field
// by field into its proto mirror and back. The failure mode that discipline has
// is not a wrong value, it is an ABSENT STATEMENT — a field nobody remembered
// to copy (or that has no proto field at all). No coverage, mutation, or
// complexity metric can point at a line that was never written, and neither can
// a test that names the fields it cares about: the round-trip test that lived
// here before asserted `req.MCPServers == back.MCPServers`, so MCPServers
// survived because it was NAMED, not because the type was covered. Five fields
// (ManagedConfig.Skills/DenyTools, Hook.PreToolFallback, ChatRequest.Runtime/
// ResumeSessionID) plus ChatSessionInfo.SessionID/Resumable were silently
// dropped underneath it.
//
// The only assertion that catches an absent statement is TOTAL equality of a
// FULLY-POPULATED value: fill every field, at every depth, with a
// distinguishable non-zero value discovered by REFLECTION (so a field added
// tomorrow is covered without anyone updating this file), round-trip it, and
// require the whole struct back. A zero value proves nothing about a dropped
// field — a dropped zero equals a carried zero.
//
// Anything reflection cannot populate or cannot round-trip (unexported fields,
// interfaces, oneof unions) must be handled EXPLICITLY: the filler fails loudly
// on a kind it cannot reach, unions are enumerated variant by variant, and the
// only escape hatch is parityExclusions — a named field with a written reason,
// which TestArch_ProtoConverters_ExclusionsAreLive proves still exists.
// ---------------------------------------------------------------------------

// parityExclusions names every field the total-parity sweep deliberately does
// NOT require to survive a round trip, keyed by "<pkg>.<Type>.<Field>" with the
// reason it is exempt. This is the ONLY way a field escapes the sweep, and
// TestArch_ProtoConverters_ExclusionsAreLive fails if an entry here never matches a real
// field — so a rename or deletion cannot leave a stale exemption behind,
// silently un-covering whatever field inherits the name.
//
// A field belongs here only when it is STRUCTURALLY not wire-carried. "The
// converter forgot it" is a bug to fix, never an entry to add.
var parityExclusions = map[string]string{
	// Populated on the PLUGIN side, after the wire crossing: the claude backend
	// sets req.ModelQuirk itself in its StructuredChat entry point
	// (internal/claude/chat.go, D-CO-QUIRK) from a package-level constant. The
	// host never sets it, so there is nothing for chatStartToProto to carry —
	// mirroring it onto the wire would let a host dictate a non-spec JSON-RPC
	// method to a plugin, which is strictly worse than leaving it plugin-local.
	"agent.ChatRequest.ModelQuirk": "set plugin-side by the backend (internal/claude/chat.go), never sent host→plugin",
	// In-process only, by construction and by doc: wire.Hook.ContextHash marks
	// the context-injection hook, and ManagedConfig's hooks are explicitly the
	// set WITHOUT context-injection (the plugin appends its own from its
	// plugin-side context hash). The field is tagged `mapstructure:"-"
	// yaml:"-" json:"-"` precisely so it never serializes anywhere.
	"wire.Hook.ContextHash": "in-process only (yaml/json/mapstructure `-`); the plugin derives its own context-injection hook",
}

// parityMaxDepth bounds the reflective walk. Nothing in this package's wire
// vocabulary is self-referential; exceeding it means a type became recursive
// and this helper needs a cycle breaker, so it fails rather than hangs.
const parityMaxDepth = 12

// parityFiller populates a Go value totally and reproducibly: every exported
// field, at every depth, gets a distinguishable non-zero value derived from a
// monotonic counter, so a field dropped in either direction shows up as a
// concrete diff rather than "zero == zero".
type parityFiller struct {
	t    *testing.T
	n    int
	hits map[string]bool // exclusion paths actually encountered, for the anti-rot gate
}

func (f *parityFiller) next() int {
	f.n++
	return f.n
}

// fill sets v to a fully-populated, distinguishable value. It FAILS the test on
// anything it cannot reach rather than leaving a hole: an unreachable field is
// exactly the bug this whole file exists to catch, and a test that silently
// skips one has the same defect as the converter.
func (f *parityFiller) fill(v reflect.Value, path string, depth int) {
	f.t.Helper()
	if depth > parityMaxDepth {
		f.t.Fatalf("parity fill: exceeded depth %d at %s — the wire vocabulary became recursive; this helper needs a cycle breaker", parityMaxDepth, path)
	}

	// Types whose round trip is lossy or enumerated get an explicit,
	// round-trippable value rather than an arbitrary one.
	switch v.Type() {
	case reflect.TypeOf(time.Time{}):
		// Times cross as unix SECONDS (sessionhistory.go's timeToUnix), so a
		// sub-second or non-UTC value could never round-trip; pick one that
		// legitimately can, and keep it non-zero (zero encodes as "unset").
		v.Set(reflect.ValueOf(time.Unix(int64(1700000000+f.next()), 0).UTC()))
		return
	case reflect.TypeOf(agent.PermissionMode(0)):
		// Crosses as its String() spelling, so only a named mode round-trips.
		v.Set(reflect.ValueOf(agent.PermissionAcceptEdits))
		return
	case reflect.TypeOf(agent.CellKind(0)):
		// Enum with a documented default; pick a non-default member.
		v.Set(reflect.ValueOf(agent.CellKindDirectoryIsolated))
		return
	case reflect.TypeOf(os.FileMode(0)):
		// The exec bit is load-bearing for skill packages (scripts/).
		v.Set(reflect.ValueOf(os.FileMode(0o755)))
		return
	}

	switch v.Kind() {
	case reflect.String:
		v.SetString(fmt.Sprintf("%s#%d", path, f.next()))
	case reflect.Bool:
		// Every bool field's zero value is false, so the only distinguishable
		// population is true.
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// An ENUM's distinguishable population is a valid MEMBER, not any int.
		// The counter below would hand agent.Approach a 4 — a value the enum
		// does not define and the host side can never hold — and a converter
		// that carries enums as their stable labels then legitimately refuses
		// it, failing this test for a case that cannot occur. Filling from the
		// domain keeps the round trip about what the converter DROPS rather
		// than about what the filler invented.
		if members, ok := enumDomain(v.Type()); ok {
			v.SetInt(members[f.next()%len(members)])
			return
		}
		// Small: several converters narrow int→int32 on the way out.
		v.SetInt(int64(f.next()))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(uint64(f.next()))
	case reflect.Float32, reflect.Float64:
		v.SetFloat(float64(f.next()) + 0.5)
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 { // []byte / json.RawMessage
			v.SetBytes([]byte(fmt.Sprintf(`{"at":"%s","n":%d}`, path, f.next())))
			return
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			f.fill(s.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1)
		}
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		for i := range 2 {
			k := reflect.New(v.Type().Key()).Elem()
			f.fill(k, fmt.Sprintf("%s.k%d", path, i), depth+1)
			val := reflect.New(v.Type().Elem()).Elem()
			f.fill(val, fmt.Sprintf("%s.v%d", path, i), depth+1)
			m.SetMapIndex(k, val)
		}
		v.Set(m)
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		f.fill(p.Elem(), path, depth+1)
		v.Set(p)
	case reflect.Struct:
		st := v.Type()
		for i := range st.NumField() {
			sf := st.Field(i)
			fieldPath := st.String() + "." + sf.Name
			if _, excluded := parityExclusions[fieldPath]; excluded {
				f.hits[fieldPath] = true
				continue
			}
			if !sf.IsExported() {
				f.t.Fatalf("parity fill: %s is UNEXPORTED and reflection cannot populate it — no total-equality assertion covers it. Either export it, drop it from the wire type, or add it to parityExclusions with a written reason.", fieldPath)
			}
			f.fill(v.Field(i), fieldPath, depth+1)
		}
	default:
		// Interface, chan, func, unsafe pointer: reflection has no generic
		// non-zero value, so parity here would be a lie.
		f.t.Fatalf("parity fill: cannot populate %s (kind %s) at %s — a value this helper cannot construct is a field this helper cannot cover. Add it to parityExclusions with a written reason, or give the type a concrete case above.", path, v.Kind(), path)
	}
}

// zeroUnionExcept clears every field named in union except those in keep. It is
// how a oneof-shaped Go struct (exactly one of N pointers set) is round-tripped
// honestly: fully populating all N and asserting total equality would fail on
// the CONVERTER'S OWN CONTRACT rather than on a dropped field, so each variant
// is asserted on its own with the rest zeroed.
func zeroUnionExcept(t *testing.T, v reflect.Value, union, keep []string) {
	t.Helper()
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	require.Equal(t, reflect.Struct, v.Kind(), "union variants only make sense on a struct")
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	for _, name := range union {
		if kept[name] {
			continue
		}
		fv := v.FieldByName(name)
		require.True(t, fv.IsValid(), "union field %q does not exist on %s — the variant list is stale", name, v.Type())
		fv.Set(reflect.Zero(fv.Type()))
	}
}

// checkParity is the whole point of this file: round-trip a FULLY-POPULATED G
// through to→from and require TOTAL equality. No field is named, so a field
// added to G later is covered the moment it exists.
//
// variants, when given, enumerate a oneof union: each variant lists the fields
// that stay populated while every other union field is zeroed, and each is
// asserted as its own subtest. Fields outside the union stay fully populated in
// every variant.
func checkParity[G any, P any](t *testing.T, hits map[string]bool, name string, to func(G) P, from func(P) G, variants ...[]string) {
	t.Helper()

	union := []string{}
	seen := map[string]bool{}
	for _, variant := range variants {
		for _, f := range variant {
			if !seen[f] {
				seen[f] = true
				union = append(union, f)
			}
		}
	}

	roundTrip := func(t *testing.T, keep []string) {
		t.Helper()
		var g G
		rv := reflect.ValueOf(&g).Elem()
		f := &parityFiller{t: t, hits: hits}
		f.fill(rv, name, 0)
		if len(union) > 0 {
			zeroUnionExcept(t, rv, union, keep)
		}
		back := from(to(g))
		require.Equal(t, g, back,
			"%s: a fully-populated value did not survive the proto round trip. Every difference below is a field with NO proto field or NO converter statement — it is silently dropped on the wire in production.", name)
	}

	if len(variants) == 0 {
		t.Run(name, func(t *testing.T) { roundTrip(t, nil) })
		return
	}
	for _, variant := range variants {
		t.Run(name+"/"+fmt.Sprint(variant), func(t *testing.T) { roundTrip(t, variant) })
	}
}

// parityHits records which parityExclusions entries the sweep actually met, so
// TestArch_ProtoConverters_ExclusionsAreLive can fail on a stale one.
var parityHits = map[string]bool{}

// chatMessageFromInputOrFail adapts the production decoder's (value, ok) shape
// to the parity helper's func(P) G. ok is false only for a frame carrying no
// chat message at all, which a populated variant never produces.
func chatMessageFromInputOrFail(t *testing.T) func(*ChatInput) agent.ChatMessage {
	return func(in *ChatInput) agent.ChatMessage {
		msg, ok := chatMessageFromInput(in)
		require.True(t, ok, "chatMessageFromInput rejected a frame chatMessageToInput produced")
		return msg
	}
}

// TestArch_ProtoConverters_MirrorEveryStructField sweeps EVERY hand-mirrored converter pair in this package.
// Adding a converter pair without adding it here is the one gap this design
// cannot close by reflection — see TestArch_ProtoConverters_EveryPairIsSwept.
func TestArch_ProtoConverters_MirrorEveryStructField(t *testing.T) {
	hits := parityHits

	// --- managed.go: the host-assembled setup payload ---
	checkParity(t, hits, "agent.ManagedConfig", ManagedConfigToProto, managedConfigFromProto)
	// Swept in its own right, not only through ManagedConfig: this pair carries
	// enums as LABELS, so it is the one conversion in this package that can
	// refuse its input, and a round trip over the valid domain is what says the
	// refusal is scoped to values the host cannot hold.
	checkParity(t, hits, "map[agent.SurfaceKind]agent.Approach", surfacesToProto, surfacesFromProto)
	checkParity(t, hits, "[]agent.CommandExport", commandExportsToProto, commandExportsFromProto)
	checkParity(t, hits, "[]agent.SkillExport", skillExportsToProto, skillExportsFromProto)
	checkParity(t, hits, "[]agent.PackageFile", packageFilesToProto, packageFilesFromProto)
	checkParity(t, hits, "wire.Hook", hookToProto, hookFromProto)
	checkParity(t, hits, "[]wire.Hook", hooksToProto, hooksFromProto)
	checkParity(t, hits, "wire.UnifiedHooks", unifiedHooksToProto, unifiedHooksFromProto)
	checkParity(t, hits, "wire.HooksConfig", hooksConfigToProto, hooksConfigFromProto)
	checkParity(t, hits, "wire.MCPServer", mcpServerToProto, mcpServerFromProto)
	checkParity(t, hits, "map[string]wire.MCPServer", mcpServerMapToProto, mcpServerMapFromProto)
	checkParity(t, hits, "wire.MCPConfig", mcpConfigToProto, mcpConfigFromProto)

	// --- plans.go ---
	checkParity(t, hits, "[]agent.PlanFile", planFilesToProto, planFilesFromProto)

	// --- sessionhistory.go: normalized transcripts ---
	checkParity(t, hits, "agent.SessionEntry", EntryToProto, entryFromProto)
	checkParity(t, hits, "[]agent.ToolLocation", locationsToProto, locationsFromProto)
	checkParity(t, hits, "[]agent.ContentBlock", contentBlocksToProto, contentBlocksFromProto)
	checkParity(t, hits, "[]agent.ToolContentBlock", toolContentToProto, toolContentFromProto)
	checkParity(t, hits, "[]agent.PlanEntry", planEntriesToProto, planEntriesFromProto)
	checkParity(t, hits, "agent.Session", sessionToProto, sessionFromProto)
	checkParity(t, hits, "agent.SessionMeta", sessionMetaToProto, sessionMetaFromProto)

	// --- server.go: the isolation-cell enum ---
	checkParity(t, hits, "agent.CellKind", CellKindToProto, cellKindFromProto)

	// --- chat.go: the structured-chat transport ---
	checkParity(t, hits, "agent.ChatRequest", chatStartToProto, chatStartFromProto)
	checkParity(t, hits, "agent.TerminalRequest", terminalRequestToProto, terminalRequestFromProto)
	checkParity(t, hits, "agent.TerminalResponse", terminalAnswerToProto, terminalAnswerFromProto)
	checkParity(t, hits, "agent.PermissionRequest", permissionRequestToProto, permissionRequestFromProto)
	checkParity(t, hits, "agent.TurnMeta", turnMetaToProto, turnMetaFromProto)
	checkParity(t, hits, "agent.ChatSessionInfo", chatSessionInfoToProto, chatSessionInfoFromProto)

	// ChatEvent is a oneof: exactly one of Entry/Complete/Session/Permission/
	// Terminal is set (Raw is NOT part of the union — it may ride alongside any
	// variant, so it stays populated throughout).
	checkParity(t, hits, "agent.ChatEvent", chatEventToProto, chatEventFromProto,
		[]string{"Entry"}, []string{"Complete"}, []string{"Session"}, []string{"Permission"}, []string{"Terminal"})

	// ChatMessage is a oneof too, and its variant selector is the ORDER of
	// chatMessageToInput's switch: Permission, then CancelTurn, then Terminal,
	// then the user-turn default (Text + ContentBlocks together).
	checkParity(t, hits, "agent.ChatMessage", chatMessageToInput, chatMessageFromInputOrFail(t),
		[]string{"Permission"}, []string{"CancelTurn"}, []string{"Terminal"}, []string{"Text", "ContentBlocks"})
}

// oneWayConverters names every ToProto/FromProto function in this package that
// has NO opposite number, with the reason. A one-way converter cannot be
// round-tripped, so parity says nothing about it — listing it here is an
// admission, not a pass. TestArch_ProtoConverters_EveryPairIsSwept fails if an
// entry stops naming a real function.
var oneWayConverters = map[string]string{
	// Encode-only: the host reports a backend's model identity outward over
	// GetModelInfo and nothing decodes a ModelInfo back into agent form, so
	// there is no inverse to pair with.
	"convertModelInfoToProto": "encode-only (GetModelInfo response); no decoder exists to pair with",
}

// TestArch_ProtoConverters_EveryPairIsSwept closes the one hole reflection
// cannot: TestArch_ProtoConverters_MirrorEveryStructField is a hand-written LIST, so a converter pair added
// later is covered only if somebody remembers to add it. This walks the
// package's own source for every <x>ToProto/<x>FromProto (and ToInput/FromInput)
// pair and fails when one is missing from the sweep — turning "remember to add
// it" into a gate.
func TestArch_ProtoConverters_EveryPairIsSwept(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	require.NoError(t, err, "parse this package's own source")

	base := func(name string) string {
		for _, suffix := range []string{"ToProto", "FromProto", "ToInput", "FromInput"} {
			if strings.HasSuffix(name, suffix) {
				return strings.ToLower(strings.TrimSuffix(name, suffix))
			}
		}
		return ""
	}

	encoders, decoders := map[string]string{}, map[string]string{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Recv != nil {
					continue // methods are not converter functions
				}
				name := fd.Name.Name
				key := base(name)
				if key == "" {
					continue
				}
				if strings.HasSuffix(name, "ToProto") || strings.HasSuffix(name, "ToInput") {
					encoders[key] = name
				} else {
					decoders[key] = name
				}
			}
		}
	}
	require.NotEmpty(t, encoders, "found no converters at all — the source walk is broken, not the package")

	src, err := os.ReadFile("arch_test.go")
	require.NoError(t, err)
	sweep := string(src)
	mentioned := func(name string) bool {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`).MatchString(sweep)
	}

	var uncovered, unpaired, staleOneWay []string
	for key, enc := range encoders {
		dec, paired := decoders[key]
		if !paired {
			if _, known := oneWayConverters[enc]; !known {
				unpaired = append(unpaired, enc)
			}
			continue
		}
		if !mentioned(enc) || !mentioned(dec) {
			uncovered = append(uncovered, enc+" / "+dec)
		}
	}
	for key, dec := range decoders {
		if _, paired := encoders[key]; !paired {
			if _, known := oneWayConverters[dec]; !known {
				unpaired = append(unpaired, dec)
			}
		}
	}
	for name := range oneWayConverters {
		if base(name) == "" || (encoders[base(name)] != name && decoders[base(name)] != name) {
			staleOneWay = append(staleOneWay, name)
		}
	}
	sort.Strings(uncovered)
	sort.Strings(unpaired)
	sort.Strings(staleOneWay)

	require.Empty(t, uncovered,
		"converter pair(s) exist in this package but are NOT in TestArch_ProtoConverters_MirrorEveryStructField's sweep — every field they carry is uncovered, which is exactly how eight fields went missing under a green test. Add a checkParity line.")
	require.Empty(t, unpaired,
		"converter(s) have no opposite number and are not declared one-way — either add the inverse, or add it to oneWayConverters with a written reason.")
	require.Empty(t, staleOneWay,
		"oneWayConverters names function(s) that no longer exist — delete the stale entry so a future function inheriting the name is not silently exempted.")
}

// TestArch_ProtoConverters_ExclusionsAreLive fails when parityExclusions names a field
// the sweep never met — a stale exemption is a silently un-covered field the
// moment something else inherits the name. Runs after TestArch_ProtoConverters_MirrorEveryStructField by
// alphabetical order within the file's single sweep; it re-runs the sweep's
// population itself rather than trusting ordering.
func TestArch_ProtoConverters_ExclusionsAreLive(t *testing.T) {
	// Re-run the sweep into a private hit set so this test does not depend on
	// having run after TestArch_ProtoConverters_MirrorEveryStructField.
	hits := map[string]bool{}
	t.Run("sweep", func(t *testing.T) {
		checkParity(t, hits, "agent.ChatRequest", chatStartToProto, chatStartFromProto)
		checkParity(t, hits, "wire.Hook", hookToProto, hookFromProto)
	})

	missing := []string{}
	for path, reason := range parityExclusions {
		if !hits[path] {
			missing = append(missing, fmt.Sprintf("%s (%s)", path, reason))
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"parityExclusions names field(s) the parity sweep never encountered — either the field was renamed/removed (delete the entry) or nothing sweeps its type (add the converter pair to TestArch_ProtoConverters_MirrorEveryStructField). A stale exemption silently un-covers whatever inherits the name.")
}

// enumDomain reports the valid values of the enum types this package's
// converters carry BY LABEL rather than by number. Carrying labels is what
// makes an unknown value fail loudly instead of resolving to iota 0 — which for
// both of these is the least safe member (SurfaceContext, ApproachUnsafeFile) —
// and the price is that a synthetic out-of-domain int is not round-trippable.
//
// Listed from the packages' own exported enumerations, so a member added there
// widens this automatically rather than silently narrowing what gets exercised.
func enumDomain(t reflect.Type) ([]int64, bool) {
	switch t {
	case reflect.TypeOf(agent.SurfaceKind(0)):
		out := []int64{}
		for _, name := range agent.SurfaceKindNames() {
			k, err := agent.ParseSurfaceKind(name)
			if err == nil {
				out = append(out, int64(k))
			}
		}
		return out, len(out) > 0
	case reflect.TypeOf(agent.Approach(0)):
		out := []int64{}
		for _, name := range agent.ApproachNames() {
			a, err := agent.ParseApproach(name)
			if err == nil {
				out = append(out, int64(a))
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

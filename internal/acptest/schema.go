// Package acptest is the ACP conformance harness (H1 slice): it vendors the
// CURRENT Agent Client Protocol JSON Schema — deliberately newer than the
// pinned SDK (github.com/joshgarnett/agent-client-protocol-go, pinned
// 2025-09-02, see go.mod) — and validates individual wire payloads against
// it. This is TEST/HARNESS INFRASTRUCTURE: it is never imported by cmd/ctxloom
// or any production request path; it exists to measure the gap between what
// ctxloom emits and what the current spec defines, so the next slice (SDK1,
// which re-vendors the SDK) has an honest, evidence-based acceptance
// checklist rather than a guess.
//
// Provenance of acp-schema-v1.json:
//
//	Source:  https://github.com/agentclientprotocol/agent-client-protocol
//	Path:    schema/v1/schema.json (the STABLE v1 schema — matches the
//	         protocolVersion=1 ctxloom negotiates; schema/v2 is an unreleased,
//	         breaking protocol redesign — session/new -> session/resume,
//	         auth/login, session/close/delete/list — and is NOT what ctxloom
//	         speaks, so validating against it would misreport every v1 frame
//	         as broken).
//	Commit:  a34b896504dd86136f80aab0e69de7a77bacc181 (2026-07-06)
//	Version: schema-v1.19.0 (schema/v1/CHANGELOG.md), i.e. 10+ months of
//	         schema evolution AHEAD of the pinned SDK's 2025-09-02 snapshot.
//	Vendored: 2026-07-16.
//
// Re-vendor by fetching the branch tip, then RE-RECORDING what you fetched —
// the fetch alone leaves every line of the block above describing bytes that
// are no longer here, and the schema file carries no version marker of its
// own to contradict them:
//
//  1. curl -sL -o internal/acptest/acp-schema-v1.json \
//     https://raw.githubusercontent.com/agentclientprotocol/agent-client-protocol/main/schema/v1/schema.json
//  2. Update Commit / Version / Vendored above from what you actually
//     fetched: the upstream commit id `main` resolved to, the version at the
//     head of schema/v1/CHANGELOG.md, and today's date.
//  3. Update vendoredSchemaSHA256 (schema_provenance_test.go) to the new
//     content hash. That test fails until you do, which is what stops step 2
//     from being skipped.
package acptest

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

//go:embed acp-schema-v1.json
var schemaJSON []byte

// U015-F02: SchemaSourceURL/SchemaCommit/SchemaVersion/SchemaVendoredAt used
// to be exported here too, documenting the vendored schema's provenance --
// but the same provenance is already stated, in more detail, in the package
// doc comment above (source URL, commit, version, vendor date), and these
// four constants had zero readers anywhere in the repo, including this
// package's own tests. Deleted rather than left as an unenforced,
// independently-driftable second copy of the same facts (nothing checked
// that a re-vendor updated both the file's provenance comment AND these
// constants together — see the package doc's re-vendor recipe, which only
// updates the JSON file).
//
// schemaResourceName is the compiler resource id acp-schema-v1.json is
// registered under; $defs entries are addressed as
// schemaResourceName + "#/$defs/<Name>".
const schemaResourceName = "acp-schema-v1.json"

// Validator validates individual ACP wire payloads (a request's params, a
// response's result, a notification's params) against the current spec's
// named $defs schema — NOT the whole JSON-RPC envelope, so a failure names
// the exact ACP type that diverged (e.g. "NewSessionResponse") rather than a
// generic "no branch of this oneOf matched" from the top-level union.
type Validator struct {
	// mu guards BOTH the cache map and the compiler. The compiler needs it
	// as much as the map does: jsonschema.Compiler mutates its own resource
	// table inside Compile (findResource) and documents no concurrency
	// guarantee, so two goroutines compiling different $defs of the same
	// resource race each other inside the library.
	mu       sync.Mutex
	compiler *jsonschema.Compiler
	cache    map[string]*jsonschema.Schema
}

// NewValidator compiles the vendored schema AS VENDORED. Safe for concurrent
// use after construction (Validate compiles each $defs entry once and caches
// it).
//
// As vendored, the schema is PERMISSIVE about unknown properties: not one of
// its 142 $defs sets `additionalProperties: false` (the keyword appears 100
// times and every occurrence is a `_meta` bag set to `true`). So this
// Validator detects a MISSING required field but never an EXTRA or MISSPELLED
// one. That is upstream's deliberate choice, not an oversight — see
// NewStrictValidator for the opt-in that measures the other half.
func NewValidator() (*Validator, error) {
	return newValidator(schemaJSON)
}

// NewStrictValidator compiles the SAME vendored bytes with unknown-property
// checking switched on: every object schema that enumerates its `properties`
// is closed with `unevaluatedProperties: false` before compilation, so a
// payload carrying a field the spec does not define is reported instead of
// silently accepted.
//
// WHY `unevaluatedProperties` AND NOT `additionalProperties`: 28 of
// schema-v1.19.0's nodes pair an enumerated `properties` with a sibling
// `allOf`/`anyOf`/`oneOf` — every SessionUpdate variant, for one, declares
// only its own `sessionUpdate` discriminator and inherits the payload fields
// from an `allOf: [$ref]`. `additionalProperties` cannot see across a
// composition keyword, so closing those nodes with it would reject the
// inherited fields and manufacture divergences that are artifacts of the
// transform rather than facts about ctxloom. `unevaluatedProperties`
// (draft 2020-12, which is what this schema declares) is exactly the
// composition-aware form. See
// TestStrictSchema_CompositionSurvivesTheClose.
//
// WHY THE TRANSFORM IS IN MEMORY AND NOT IN THE FILE: acp-schema-v1.json is a
// byte-for-byte vendored copy of upstream (see the package doc's provenance
// block and re-vendor recipe). Editing `additionalProperties` into those bytes
// would (a) assert a constraint the ACP spec does not impose on its peers and
// (b) be silently dropped by the next `curl` re-vendor — a fix that deletes
// itself. Deriving the strict form at construction time keeps the vendored
// bytes authoritative and the strictness durable.
//
// WHAT STAYS OPEN: `_meta`, which already states `additionalProperties: true`
// in the vendored bytes and is the spec's own sanctioned extension channel.
// The transform only fills the keyword in where upstream left it unstated, so
// an extension riding `_meta` is still valid under a strict Validator — which
// is the point: strictness is meant to catch fields invented OUTSIDE the
// extension channel.
//
// This is OPT-IN because ctxloom knowingly emits one such field today; see
// internal/acpagent/l0_conformance_test.go's l0KnownDivergences.
func NewStrictValidator() (*Validator, error) {
	strictJSON, err := strictSchemaJSON()
	if err != nil {
		return nil, err
	}
	return newValidator(strictJSON)
}

func newValidator(schema []byte) (*Validator, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceName, bytes.NewReader(schema)); err != nil {
		return nil, fmt.Errorf("acptest: add schema resource: %w", err)
	}
	return &Validator{compiler: compiler, cache: make(map[string]*jsonschema.Schema)}, nil
}

// def compiles (or returns the cached compilation of) the named $defs entry.
//
// The lock spans the compile, not just the map access, because the compiler
// itself is shared mutable state (see Validator.mu). Compilation is held once
// per $defs entry for the process's lifetime, so the serialisation costs at
// most one compile per name — a conformance run validating many frames
// concurrently pays it on the first frame of each type and never again.
func (v *Validator) def(name string) (*jsonschema.Schema, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.cache[name]; ok {
		return s, nil
	}
	s, err := v.compiler.Compile(schemaResourceName + "#/$defs/" + name)
	if err != nil {
		return nil, fmt.Errorf("acptest: compile $defs/%s: %w", name, err)
	}
	v.cache[name] = s
	return s, nil
}

// ValidateDef validates raw (a JSON payload — a request's params, a
// response's result, or a notification's params) against the named $defs
// entry of the current ACP schema (e.g. "NewSessionResponse",
// "SessionNotification", "RequestPermissionRequest"). A nil/empty raw is
// validated as JSON null (the same thing a real ACP peer would receive for an
// omitted params/result field), so an accidentally-empty frame is measured
// honestly rather than skipped.
//
// Whether an UNKNOWN property is a failure depends on which constructor built
// this Validator: NewValidator accepts it (upstream's own permissiveness),
// NewStrictValidator reports it.
func (v *Validator) ValidateDef(defName string, raw json.RawMessage) error {
	schema, err := v.def(defName)
	if err != nil {
		return err
	}
	var payload interface{}
	if len(raw) == 0 {
		raw = json.RawMessage("null")
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("acptest: payload is not valid JSON: %w", err)
	}
	return schema.Validate(payload)
}

// strictSchemaJSON derives the closed form of the vendored schema: a deep copy
// with `"unevaluatedProperties": false` filled in at every INSTANCE-LOCATION
// ROOT that could describe an object.
//
// The vendored bytes are never touched — this reads the embedded copy and
// returns new bytes.
func strictSchemaJSON() ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(schemaJSON))
	// Numbers stay verbatim; nothing here inspects them, and round-tripping
	// through float64 is a needless way to alter a `minimum`.
	dec.UseNumber()
	var doc interface{}
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("acptest: decode vendored schema: %w", err)
	}
	mixins := map[string]bool{}
	collectMixinRefs(doc, mixins, false)
	closeInstanceRoots(doc, mixins, true)
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("acptest: re-encode strict schema: %w", err)
	}
	return out, nil
}

// JSON Schema applicator keywords, split by WHERE the subschemas they hold
// apply. Walking by keyword rather than "recurse into every nested object" is
// what keeps the transform off non-schema data — a `const`/`default`/`enum`
// object is a VALUE, not a schema.
//
//   - childMapKeywords / childKeywords hold subschemas that apply to a CHILD
//     instance location (a property's value, an array element). Each such
//     subschema is the outermost schema for that location, so that is where a
//     close belongs.
//   - inPlaceMapKeywords / inPlaceKeywords hold subschemas that apply to the
//     SAME instance location as their parent. Closing one of those is wrong:
//     it sees only its own branch's properties, not the ones its siblings and
//     its parent contribute.
var (
	childMapKeywords   = []string{"properties", "patternProperties"}
	childKeywords      = []string{"items", "prefixItems", "additionalProperties", "contains", "unevaluatedProperties"}
	inPlaceMapKeywords = []string{"dependentSchemas"}
	inPlaceKeywords    = []string{"allOf", "anyOf", "oneOf", "not", "if", "then", "else", "propertyNames"}
)

// defsPointerPrefix is the local JSON-pointer prefix every $ref in this schema
// uses; a ref through it names one of the document's $defs entries.
const defsPointerPrefix = "#/$defs/"

// collectMixinRefs records every $defs entry used as a MIXIN: $ref'd from a
// position where it supplies only PART of the properties its instance
// location may carry, with the rest coming from schemas it is composed with.
// `allOf: [{"$ref": ...}]` beside a sibling `properties` is how
// schema-v1.19.0 spells that — SessionUpdate's user_message_chunk variant
// declares only its own `sessionUpdate` discriminator and pulls `content` in
// from ContentChunk that way.
//
// Mixins must be left OPEN. Closing one makes it reject the properties its
// composer contributes, which is a fact about the transform, not about
// ctxloom. Their instance locations are still covered: the close sits on the
// composer's outermost schema, and `unevaluatedProperties` sees straight
// through the composition to everything the mixin evaluated.
//
// A lone UNION branch is deliberately NOT a mixin. `anyOf: [{"$ref": A},
// {"$ref": B}]` on a node that contributes nothing of its own picks one WHOLE
// schema for the location, so A is that location's outermost schema whenever
// it matches, and closing it is both safe and necessary — AgentResponse's
// `result` reaches NewSessionResponse exactly that way, and treating it as a
// mixin means the strict harness never checks a single response body.
//
// shared says whether the node being visited already shares its instance
// location with something that contributes properties; it is how a mixin two
// composition levels down (the JSON-RPC envelope's
// `anyOf/allOf/$ref AgentRequest`, under a parent that names `jsonrpc`) is
// still recognised as one.
func collectMixinRefs(node interface{}, out map[string]bool, shared bool) {
	n, isMap := node.(map[string]interface{})
	if !isMap {
		return
	}
	_, enumerates := n["properties"]
	allOf, _ := n["allOf"].([]interface{})
	_, hasAnyOf := n["anyOf"]
	_, hasOneOf := n["oneOf"]
	hasUnion := hasAnyOf || hasOneOf

	// An allOf member always applies together with its siblings, the node's
	// own properties, and any union branch — so it is composed unless it is
	// the node's only content.
	allOfShared := shared || enumerates || len(allOf) > 1 || hasUnion
	for _, member := range allOf {
		noteRefTarget(member, out, allOfShared)
		collectMixinRefs(member, out, allOfShared)
	}
	// A union branch applies alone, so it is composed only with the node's own
	// properties and any allOf mixed in alongside.
	unionShared := shared || enumerates || len(allOf) > 0
	for _, kw := range []string{"anyOf", "oneOf"} {
		branches, _ := n[kw].([]interface{})
		for _, branch := range branches {
			noteRefTarget(branch, out, unionShared)
			collectMixinRefs(branch, out, unionShared)
		}
	}
	for _, kw := range []string{"not", "if", "then", "else"} {
		noteRefTarget(n[kw], out, true)
		collectMixinRefs(n[kw], out, true)
	}
	if dependents, isDependentMap := n["dependentSchemas"].(map[string]interface{}); isDependentMap {
		for _, sub := range dependents {
			noteRefTarget(sub, out, true)
			collectMixinRefs(sub, out, true)
		}
	}

	// Child instance locations start a fresh composition: whatever the parent
	// contributed applies to the parent's object, not to this one.
	for _, kw := range childKeywords {
		switch child := n[kw].(type) {
		case []interface{}:
			for _, item := range child {
				collectMixinRefs(item, out, false)
			}
		default:
			collectMixinRefs(child, out, false)
		}
	}
	for _, kw := range append(append([]string{}, childMapKeywords...), "$defs") {
		if m, isChildMap := n[kw].(map[string]interface{}); isChildMap {
			for _, sub := range m {
				collectMixinRefs(sub, out, false)
			}
		}
	}
}

// noteRefTarget records node's local $defs ref target when that reference sits
// in a composed position.
func noteRefTarget(node interface{}, out map[string]bool, shared bool) {
	if !shared {
		return
	}
	n, isMap := node.(map[string]interface{})
	if !isMap {
		return
	}
	ref, isString := n["$ref"].(string)
	if isString && strings.HasPrefix(ref, defsPointerPrefix) {
		out[strings.TrimPrefix(ref, defsPointerPrefix)] = true
	}
}

// closeInstanceRoots walks the schema and fills in
// `unevaluatedProperties: false` on every node that is the OUTERMOST schema
// for some instance location — the document root, each `$defs` entry that is
// not a mixin (see collectMixinRefs), and every property/array-element
// subschema.
//
// A node that already STATES `additionalProperties` or
// `unevaluatedProperties` is left exactly as upstream wrote it. That is what
// keeps `_meta` — the spec's own sanctioned extension channel, which declares
// `additionalProperties: true` — open under a strict Validator.
func closeInstanceRoots(node interface{}, mixins map[string]bool, isInstanceRoot bool) {
	switch n := node.(type) {
	case []interface{}:
		for _, item := range n {
			closeInstanceRoots(item, mixins, isInstanceRoot)
		}
	case map[string]interface{}:
		if isInstanceRoot && couldDescribeObject(n) && !statesOpenness(n) {
			n["unevaluatedProperties"] = false
		}
		for _, kw := range childKeywords {
			closeInstanceRoots(n[kw], mixins, true)
		}
		for _, kw := range childMapKeywords {
			if m, isMap := n[kw].(map[string]interface{}); isMap {
				for _, sub := range m {
					closeInstanceRoots(sub, mixins, true)
				}
			}
		}
		for _, kw := range inPlaceKeywords {
			closeInstanceRoots(n[kw], mixins, false)
		}
		for _, kw := range inPlaceMapKeywords {
			if m, isMap := n[kw].(map[string]interface{}); isMap {
				for _, sub := range m {
					closeInstanceRoots(sub, mixins, false)
				}
			}
		}
		if defs, isMap := n["$defs"].(map[string]interface{}); isMap {
			for name, sub := range defs {
				closeInstanceRoots(sub, mixins, !mixins[name])
			}
		}
	}
}

// statesOpenness reports whether upstream already said what this node does
// with properties it does not name.
func statesOpenness(n map[string]interface{}) bool {
	if _, stated := n["additionalProperties"]; stated {
		return true
	}
	_, stated := n["unevaluatedProperties"]
	return stated
}

// couldDescribeObject reports whether a close on this node could ever bite —
// it names properties, defers to another schema, or admits the object type.
// Skipping the rest keeps the derived schema readable when it is dumped for
// debugging, and keeps the injected keyword count meaningful.
func couldDescribeObject(n map[string]interface{}) bool {
	if _, has := n["properties"]; has {
		return true
	}
	if _, has := n["$ref"]; has {
		return true
	}
	for _, kw := range inPlaceKeywords {
		if _, has := n[kw]; has {
			return true
		}
	}
	switch t := n["type"].(type) {
	case string:
		return t == "object"
	case []interface{}:
		for _, entry := range t {
			if s, isString := entry.(string); isString && s == "object" {
				return true
			}
		}
	}
	return false
}

package acptest

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidator_KnownGoodAndBad pins the validator's own behavior against two
// unambiguous cases from the current schema, so a regression in the harness
// itself (e.g. a compiler misconfiguration that accepts everything) fails
// loudly here rather than silently passing every L0 conformance test.
func TestValidator_KnownGoodAndBad(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	t.Run("valid NewSessionResponse passes", func(t *testing.T) {
		err := v.ValidateDef("NewSessionResponse", json.RawMessage(`{"sessionId":"abc"}`))
		assert.NoError(t, err)
	})

	t.Run("NewSessionResponse missing required sessionId fails", func(t *testing.T) {
		err := v.ValidateDef("NewSessionResponse", json.RawMessage(`{"modes":{}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sessionId")
	})

	t.Run("null result fails an object-typed response schema", func(t *testing.T) {
		// This is the exact shape ctxloom's fs/write_text_file response takes
		// (internal/acp/jsonrpc.marshalResult renders a nil result as JSON
		// null) — pinned here so the L0 divergence it feeds stays honest.
		err := v.ValidateDef("WriteTextFileResponse", json.RawMessage(`null`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "expected object, but got null")
	})

	t.Run("empty raw validates as null, not skipped", func(t *testing.T) {
		err := v.ValidateDef("WriteTextFileResponse", nil)
		require.Error(t, err, "an empty payload must be measured, never silently treated as valid")
	})
}

// TestStrictValidator_CatchesUnknownProperties pins the OPT-IN half of the
// harness: the vendored schema alone cannot see an extra or misspelled field
// (zero of its 142 $defs close their object shapes — see
// TestVendoredSchema_IsPermissiveAboutUnknownProperties below), so a Validator
// built by NewStrictValidator is the only thing in this package that measures
// that class of divergence. Both directions are asserted on the SAME payload,
// because a strict verdict is only meaningful next to the lenient one it
// differs from.
func TestStrictValidator_CatchesUnknownProperties(t *testing.T) {
	lenient, err := NewValidator()
	require.NoError(t, err)
	strict, err := NewStrictValidator()
	require.NoError(t, err)

	// `models` is ctxloom's real, deliberate divergence (internal/acpagent/
	// wire.go's newSessionResult) — used here so this test's subject is the
	// actual field the divergence list records, not an invented one.
	withUnknown := json.RawMessage(`{"sessionId":"abc","models":{"currentModelId":"primary","availableModels":[]}}`)

	assert.NoError(t, lenient.ValidateDef("NewSessionResponse", withUnknown),
		"as vendored, the schema does not police unknown properties — if this ever fails, upstream closed its shapes and the strict transform is redundant")

	err = strict.ValidateDef("NewSessionResponse", withUnknown)
	require.Error(t, err, "a strict Validator must report a field the spec does not define")
	assert.Contains(t, err.Error(), "models", "the error must NAME the offending property, or a divergence list entry cannot be written against it")

	t.Run("a nested unknown property is caught too", func(t *testing.T) {
		// Depth matters: the transform walks subschemas, so a typo inside
		// SessionModeState must be caught the same way a top-level one is.
		err := strict.ValidateDef("NewSessionResponse",
			json.RawMessage(`{"sessionId":"abc","modes":{"currentModeId":"default","availableModes":[],"typoed":1}}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "typoed")
	})

	t.Run("a spec-valid payload still passes under strict", func(t *testing.T) {
		assert.NoError(t, strict.ValidateDef("NewSessionResponse", json.RawMessage(`{"sessionId":"abc"}`)))
	})

	t.Run("_meta stays open under strict", func(t *testing.T) {
		// The spec's own sanctioned extension channel already declares
		// additionalProperties:true, and the transform must not overwrite a
		// stated keyword — otherwise strictness would forbid the one place
		// extensions are supposed to live.
		assert.NoError(t, strict.ValidateDef("NewSessionResponse",
			json.RawMessage(`{"sessionId":"abc","_meta":{"ctxloom_anything":{"nested":true}}}`)),
			"an extension riding _meta is spec-sanctioned and must survive strict validation")
	})

	t.Run("a missing required field still fails under strict", func(t *testing.T) {
		err := strict.ValidateDef("NewSessionResponse", json.RawMessage(`{}`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sessionId", "strictness must ADD a check, never replace the required-field one")
	})
}

// TestVendoredSchema_IsPermissiveAboutUnknownProperties measures the premise
// NewStrictValidator exists for, so it cannot rot into folklore: as vendored,
// schema-v1.19.0 never closes an object shape, and every occurrence of the
// keyword is a `_meta` bag left deliberately open. If a re-vendor changes
// either number, the strict transform's job changed with it.
func TestVendoredSchema_IsPermissiveAboutUnknownProperties(t *testing.T) {
	assert.Equal(t, 0, bytes.Count(schemaJSON, []byte(`"additionalProperties": false`)),
		"the vendored schema closes an object shape now — re-check whether NewStrictValidator's transform still adds anything")
	assert.Equal(t, 100, bytes.Count(schemaJSON, []byte(`"additionalProperties": true`)),
		"the count of deliberately-open bags moved; confirm they are all still _meta before trusting strict verdicts")
}

// inPlacePosition matches a walkSchemaNodes path whose node applies to the
// SAME instance location as its parent — a member of a composition list, or a
// not/if/then/else subschema.
var inPlacePosition = regexp.MustCompile(`(/(allOf|anyOf|oneOf)/\d+|/(not|if|then|else|propertyNames))$`)

// walkSchemaNodes visits every SCHEMA node reachable from the vendored
// document, in the same keyword-driven way closeInstanceRoots does — so a
// measurement taken here counts the same population the transform acts on,
// not "every nested JSON object", which would sweep in `const`/`default`
// values that are data rather than schemas.
func walkSchemaNodes(t *testing.T, raw []byte, visit func(path string, n map[string]interface{})) {
	t.Helper()
	var doc interface{}
	require.NoError(t, json.Unmarshal(raw, &doc))

	var walk func(node interface{}, path string)
	walk = func(node interface{}, path string) {
		switch n := node.(type) {
		case []interface{}:
			for i, item := range n {
				walk(item, path+"/"+strconv.Itoa(i))
			}
		case map[string]interface{}:
			visit(path, n)
			for _, kw := range append(append([]string{}, childMapKeywords...), inPlaceMapKeywords...) {
				if m, isMap := n[kw].(map[string]interface{}); isMap {
					for name, sub := range m {
						walk(sub, path+"/"+kw+"/"+name)
					}
				}
			}
			if defs, isMap := n["$defs"].(map[string]interface{}); isMap {
				for name, sub := range defs {
					walk(sub, path+"/$defs/"+name)
				}
			}
			for _, kw := range append(append([]string{}, childKeywords...), inPlaceKeywords...) {
				if _, present := n[kw]; present {
					walk(n[kw], path+"/"+kw)
				}
			}
		}
	}
	walk(doc, "#")
}

// TestStrictSchema_CompositionSurvivesTheClose records WHY the transform
// injects `unevaluatedProperties` at instance-location roots rather than the
// more obvious `additionalProperties` on everything with a `properties` map,
// and proves the distinction is load-bearing rather than stylistic.
//
// `additionalProperties` cannot see across a composition keyword. 28 of
// schema-v1.19.0's nodes pair an enumerated `properties` with a sibling
// `allOf`/`anyOf`/`oneOf` — SessionUpdate's user_message_chunk variant, for
// one, declares only its own `sessionUpdate` discriminator and pulls
// `content` in from `allOf: [$ref ContentChunk]`. Closing those nodes the
// naive way rejects the composed fields, manufacturing divergences that are
// artifacts of the transform rather than facts about ctxloom.
func TestStrictSchema_CompositionSurvivesTheClose(t *testing.T) {
	var composed []string
	walkSchemaNodes(t, schemaJSON, func(path string, n map[string]interface{}) {
		if _, enumerates := n["properties"]; !enumerates {
			return
		}
		for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
			if _, isComposed := n[kw]; isComposed {
				composed = append(composed, path+" (properties + "+kw+")")
			}
		}
	})
	assert.NotEmpty(t, composed,
		"no node combines `properties` with a composition keyword any more — the transform could go back to the simpler `additionalProperties: false`")
	assert.Contains(t, composed, "#/$defs/SessionUpdate/oneOf/0 (properties + allOf)",
		"the worked example the rest of this test demonstrates against moved; re-derive it")

	// A REAL frame ctxloom emits, valid per the spec: the notification
	// contributes `sessionId`, the matched oneOf variant contributes
	// `sessionUpdate`, and the $ref'd ContentChunk contributes `content`.
	userChunk := json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"}}}`)

	strict, err := NewStrictValidator()
	require.NoError(t, err)
	assert.NoError(t, strict.ValidateDef("SessionNotification", userChunk),
		"a spec-valid composed frame must survive strict validation, or the harness reports transform artifacts as ctxloom divergences")

	// ...and strictness still bites INSIDE the composition: an unknown field
	// on the variant is caught even though the variant itself is never the
	// node that carries the close.
	err = strict.ValidateDef("SessionNotification",
		json.RawMessage(`{"sessionId":"s1","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"hi"},"bogus":1}}`))
	require.Error(t, err, "strictness must reach into composed subschemas, not just the outermost object")
	assert.Contains(t, err.Error(), "bogus")

	// The counterfactual: the SAME closes spelled with `additionalProperties`.
	// Derived from the real strict bytes by swapping the keyword, so the two
	// differ in nothing else.
	strictJSON, err := strictSchemaJSON()
	require.NoError(t, err)
	naiveJSON := bytes.ReplaceAll(strictJSON, []byte(`"unevaluatedProperties":false`), []byte(`"additionalProperties":false`))
	require.NotEqual(t, strictJSON, naiveJSON, "the keyword swap must actually have matched something")
	naive, err := newValidator(naiveJSON)
	require.NoError(t, err)
	require.Error(t, naive.ValidateDef("SessionNotification", userChunk),
		"if `additionalProperties` now accepts composed fields too, this whole justification is stale and the transform can be simplified")
}

// TestStrictSchema_ClosesEveryInstanceRoot confirms the transform actually
// fired, rather than silently no-opping (this repo's characteristic failure
// mode is a successful-looking run that changed nothing), and that it fired
// ONLY where it was supposed to: no schema node is added or dropped, no
// upstream statement of openness is overwritten, and nothing is closed at an
// in-place applicator position where it would fight its own composer.
func TestStrictSchema_ClosesEveryInstanceRoot(t *testing.T) {
	strictJSON, err := strictSchemaJSON()
	require.NoError(t, err)

	countNodes := func(raw []byte) (nodes, closed, open int) {
		walkSchemaNodes(t, raw, func(_ string, n map[string]interface{}) {
			nodes++
			if closedHere, isBool := n["unevaluatedProperties"].(bool); isBool && !closedHere {
				closed++
			}
			if openHere, isBool := n["additionalProperties"].(bool); isBool && openHere {
				open++
			}
		})
		return nodes, closed, open
	}

	vendoredNodes, vendoredClosed, vendoredOpen := countNodes(schemaJSON)
	strictNodes, strictClosed, strictOpen := countNodes(strictJSON)

	assert.Equal(t, 0, vendoredClosed, "sanity: the vendored bytes close nothing")
	assert.Equal(t, vendoredNodes, strictNodes, "the transform must annotate nodes, never add or drop them")
	assert.Positive(t, strictClosed, "the transform closed nothing at all — strictness would be a silent no-op")
	assert.Equal(t, vendoredOpen, strictOpen, "the deliberately-open `_meta` bags must be left exactly as upstream stated them")

	// Nothing may be closed at an in-place applicator position: a oneOf/allOf
	// member, or a $defs entry used as a mixin, sees only its own branch's
	// properties.
	mixins := map[string]bool{}
	var doc interface{}
	require.NoError(t, json.Unmarshal(schemaJSON, &doc))
	collectMixinRefs(doc, mixins, false)
	require.NotEmpty(t, mixins, "schema-v1.19.0 composes via $ref mixins; finding none means collectMixinRefs stopped working")

	var wronglyClosed []string
	walkSchemaNodes(t, strictJSON, func(path string, n map[string]interface{}) {
		closedHere, isBool := n["unevaluatedProperties"].(bool)
		if !isBool || closedHere {
			return
		}
		// The path's TRAILING segments say where this node sits: a member of
		// an allOf/anyOf/oneOf list ends "/allOf/3", a not/if/then/else
		// subschema ends with the keyword itself. Deeper occurrences are
		// irrelevant — "#/$defs/X/anyOf/0/properties/id" is a CHILD location
		// inside a branch, and closing it is exactly right.
		if inPlacePosition.MatchString(path) {
			wronglyClosed = append(wronglyClosed, path)
			return
		}
		if name, isDef := strings.CutPrefix(path, "#/$defs/"); isDef && !strings.Contains(name, "/") && mixins[name] {
			wronglyClosed = append(wronglyClosed, path+" (used as a mixin)")
		}
	})
	assert.Empty(t, wronglyClosed, "closed at an in-place applicator position, where the close cannot see its composer's properties")
}

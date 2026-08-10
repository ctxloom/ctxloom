package bundles

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/resources"
)

// fragment-schema.json is the hand-authored contract for fragment YAML. There is
// no single Go struct that fully owns the standalone-fragment file shape, but
// BundleFragment is the concrete struct whose fields appear in fragment content,
// so every serializable BundleFragment field must be permitted by the schema —
// with additionalProperties:false, a field the schema omits would make an
// otherwise-valid fragment fail validation. This is the input-schema drift gate
// for fragments (the authored-input counterpart to the generated output checks).
func TestFragmentSchema_PermitsEveryBundleFragmentField(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	var doc struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.AdditionalProperties,
		"fragment-schema additionalProperties must be false so unknown keys are rejected")

	rt := reflect.TypeFor[BundleFragment]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		_, ok := doc.Properties[name]
		assert.Truef(t, ok,
			"BundleFragment has a serializable %q field that fragment-schema does not permit; "+
				"add it to resources/schema/input/fragment-schema.json.", name)
	}
}

// TestFragmentSchema_LLMBlockPermitsEveryEngine is the same drift gate as
// above, one level deeper: LLMExports (internal/bundles/loader_content.go) is
// the Go struct backing a bundle item's `llm:` export-settings block, and it
// already carries all four engines (claude-code, codex, kiro,
// opencode). fragment-schema.json's own `llm` property only listed
// claude-code and opencode — with additionalProperties:false one level down,
// an otherwise-valid `llm: { codex: {...} }` / `kiro:` / `antigravity:` block
// failed schema validation even though the Go loader accepted it fine
// (rabid-daily). This walks LLMExports' own yaml tags, the same reflection
// approach the top-level test above uses, so a sixth engine added to the Go
// struct without a matching schema entry fails here too.
func TestFragmentSchema_LLMBlockPermitsEveryEngine(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	var doc struct {
		Properties struct {
			LLM struct {
				AdditionalProperties bool                       `json:"additionalProperties"`
				Properties           map[string]json.RawMessage `json:"properties"`
			} `json:"llm"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.False(t, doc.Properties.LLM.AdditionalProperties,
		"fragment-schema's llm block additionalProperties must be false so an unknown engine key is rejected")

	rt := reflect.TypeFor[LLMExports]()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("yaml")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			continue
		}
		_, ok := doc.Properties.LLM.Properties[name]
		assert.Truef(t, ok,
			"LLMExports has a %q engine field that fragment-schema's llm block does not permit; "+
				"add it to resources/schema/input/fragment-schema.json.", name)
	}
}

// TestFragmentSchema_ValidatesLLMBlockForEveryEngine is the payload-level
// counterpart to the reflection check above: it compiles the real schema and
// validates an actual fragment document carrying an `llm:` block for every
// engine, proving the schema doesn't merely list the property name but truly
// accepts a realistic per-engine export config (mirroring each engine's own
// Config struct in loader_content.go: codex additionally carries
// argument_hint, the others carry enabled+description only).
func TestFragmentSchema_ValidatesLLMBlockForEveryEngine(t *testing.T) {
	raw, err := resources.GetSchema("input/fragment-schema.json")
	require.NoError(t, err)

	v, err := schema.NewValidatorFromSchema(raw)
	require.NoError(t, err)

	doc := []byte(`
content: "some content"
llm:
  claude-code:
    enabled: true
    description: "Review code"
  codex:
    enabled: true
    description: "Review code"
    argument_hint: "[file]"
  kiro:
    enabled: true
    description: "Review code"
  opencode:
    enabled: true
    description: "Review code"
`)
	assert.NoError(t, v.ValidateBytes(doc),
		"a fragment's llm block naming every engine LLMExports supports must validate cleanly")
}

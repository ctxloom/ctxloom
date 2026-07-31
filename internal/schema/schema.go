// Package schema provides JSON Schema validation for config YAML files.
package schema

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/resources"
)

// ConfigValidator validates config YAML against the JSON schema.
type ConfigValidator struct {
	schema *jsonschema.Schema
}

// NewConfigValidator creates a new schema validator using the embedded ctxloom
// config schema.
func NewConfigValidator() (*ConfigValidator, error) {
	schemaData, err := resources.GetConfigSchema()
	if err != nil {
		return nil, fmt.Errorf("failed to load config schema: %w", err)
	}
	return NewValidatorFromSchema(schemaData)
}

// NewValidatorFromSchema compiles schemaData (a JSON Schema document) into a
// ConfigValidator, exactly like NewConfigValidator but for a caller whose
// schema isn't ctxloom's own embedded one — e.g. taskloom's
// resources/schema/input/taskloom-config-schema.json, read via
// resources.GetSchema. This package carries no ctxloom-specific assumption
// beyond NewConfigValidator's convenience wrapper, so a second product's
// config gets the identical validation/KnownPath machinery without a second
// implementation.
func NewValidatorFromSchema(schemaData []byte) (*ConfigValidator, error) {
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("config.json", bytes.NewReader(schemaData)); err != nil {
		return nil, fmt.Errorf("failed to add config schema resource: %w", err)
	}

	schema, err := compiler.Compile("config.json")
	if err != nil {
		return nil, fmt.Errorf("failed to compile config schema: %w", err)
	}

	return &ConfigValidator{schema: schema}, nil
}

// ValidateBytes validates YAML content against the config schema.
func (v *ConfigValidator) ValidateBytes(data []byte) error {
	var yamlData interface{}
	if err := yaml.Unmarshal(data, &yamlData); err != nil {
		return fmt.Errorf("YAML parse error: %w", err)
	}

	jsonData := convertToJSON(yamlData)
	return v.schema.Validate(jsonData)
}

// KnownPath reports whether path names a location the config schema
// recognizes, independent of whether any config currently holds a value
// there. This is the seam internal/config wires into
// internal/shared/confload.Product.KnownPath, so env/CLI override resolution
// can tell a legitimate-but-unset key (case 3: created silently) apart from a
// genuinely unrecognized one (case 4: created with a warning) -- see
// confload's resolvePath doc for the full four-outcome algorithm. A nil
// receiver or empty path is never known (degrades to false rather than
// panicking, matching this schema package's existing fault-tolerant style).
//
// Each path segment is matched against the compiled schema's own object
// keywords in order: a fixed `properties` entry, then a `patternProperties`
// regex, then an `additionalProperties` sub-schema (the dynamic-map case --
// an agent label, an LLM config label, ... -- where ANY segment is accepted
// as a valid key and the walk continues into that sub-schema for the
// segments after it). `$ref` is followed transparently. `anyOf`/`oneOf`/
// `allOf` branches (e.g. one LLM backend's config shape among several) are
// searched for the first branch that recognizes the segment.
func (v *ConfigValidator) KnownPath(path []string) bool {
	if v == nil || v.schema == nil || len(path) == 0 {
		return false
	}
	cur := v.schema
	for _, seg := range path {
		cur = schemaChild(cur, seg)
		if cur == nil {
			return false
		}
	}
	return true
}

// KnownKeys returns the declared property names of the schema object at path
// (see KnownPath's doc for the segment-resolution rule), unioned across every
// anyOf/oneOf/allOf branch reachable from it. This is the enumeration
// counterpart config/unknown_keys.go's did-you-mean suggestion needs, and it
// deliberately does NOT reuse schemaChild's "first branch that recognizes"
// policy for the union step: that policy is correct for KnownPath's yes/no
// question, but wrong here — a typo inside a kiro-typed llm.configs entry
// must be able to suggest kiro's own field names, which first-match-wins
// would never reach if an earlier branch (e.g. claude-code) also partially
// matches. Nil (not empty) when path does not resolve to anything, so a
// caller can tell "resolved, zero properties" from "did not resolve" if it
// ever needs to.
func (v *ConfigValidator) KnownKeys(path []string) []string {
	if v == nil || v.schema == nil {
		return nil
	}
	cur := v.schema
	for _, seg := range path {
		cur = schemaChild(cur, seg)
		if cur == nil {
			return nil
		}
	}
	seen := map[string]bool{}
	collectKnownKeys(cur, seen, 0)
	if len(seen) == 0 {
		return nil
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// collectKnownKeys unions s's own Properties with every anyOf/oneOf/allOf
// branch's, following $ref. depth bounds a pathological $ref cycle (the same
// unbounded-loop concern noted on resolveSchemaRef, F07) rather than
// recursing forever; 64 is far past any real schema's nesting.
func collectKnownKeys(s *jsonschema.Schema, seen map[string]bool, depth int) {
	if depth > 64 {
		return
	}
	s = resolveSchemaRef(s)
	if s == nil {
		return
	}
	for k := range s.Properties {
		seen[k] = true
	}
	for _, branches := range [][]*jsonschema.Schema{s.AnyOf, s.OneOf, s.AllOf} {
		for _, branch := range branches {
			collectKnownKeys(branch, seen, depth+1)
		}
	}
}

// resolveSchemaRef follows a chain of $ref indirection to the schema that
// actually carries the object keywords (properties/additionalProperties/...).
func resolveSchemaRef(s *jsonschema.Schema) *jsonschema.Schema {
	for s != nil && s.Ref != nil {
		s = s.Ref
	}
	return s
}

// anyKeySchema is the walker's stand-in for a location that constrains no key
// name at all, reached from `additionalProperties: true`. It is compared by
// IDENTITY, never by content: schemaChild returns it for every key, so the
// whole subtree below such a location resolves as known. It carries no
// keywords of its own and must never be handed to the validator -- it exists
// only to answer KnownPath/KnownKeys.
var anyKeySchema = &jsonschema.Schema{}

// schemaChild returns the sub-schema key names within s, or nil if s does not
// recognize key at all. See KnownPath's doc for the resolution order.
func schemaChild(s *jsonschema.Schema, key string) *jsonschema.Schema {
	if s == anyKeySchema {
		return anyKeySchema
	}
	s = resolveSchemaRef(s)
	if s == nil {
		return nil
	}
	if sub, ok := s.Properties[key]; ok {
		return resolveSchemaRef(sub)
	}
	for pattern, sub := range s.PatternProperties {
		if pattern.MatchString(key) {
			return resolveSchemaRef(sub)
		}
	}
	switch addl := s.AdditionalProperties.(type) {
	case *jsonschema.Schema:
		return resolveSchemaRef(addl)
	case bool:
		// `additionalProperties: true` says every key name here is permitted
		// and constrains the value not at all, so every segment below it is
		// permitted too. `false` says the declared names are the whole set,
		// and an ABSENT keyword is treated the same way: this walker answers
		// "does the schema NAME this location", and an author who listed
		// properties and said nothing else named exactly those. (Both in-repo
		// schemas are authored additionalProperties:false throughout, so
		// reading silence as permissive would make every typo resolve.)
		if addl {
			return anyKeySchema
		}
		return nil
	}
	if child := arrayChild(s, key); child != nil {
		return child
	}
	for _, branches := range [][]*jsonschema.Schema{s.AnyOf, s.OneOf, s.AllOf} {
		for _, branch := range branches {
			if child := schemaChild(branch, key); child != nil {
				return child
			}
		}
	}
	return nil
}

// arrayChild resolves an ARRAY INDEX segment to the schema governing that
// position, or nil when s is not an array or key is not an index. An array is
// addressed by position, so only a non-negative integer descends -- treating a
// word as an array key would make every misspelling under every array resolve
// as known.
//
// Paths reach here because a config path that crosses an array carries the
// index as a segment: config/unknown_keys.go turns the violation's instance
// location "/hooks/unified/pre_tool/0" into "hooks.unified.pre_tool.0" before
// asking for that section's declared names.
//
// Draft 2020-12 spells the positional schemas `prefixItems` and the remainder
// `items` (Items2020); earlier drafts spell the remainder `items` and allow a
// tuple form (a []*Schema). All three shapes are honored so the walker matches
// whatever draft the caller's schema declares -- NewValidatorFromSchema is a
// public seam and does not fix the draft.
func arrayChild(s *jsonschema.Schema, key string) *jsonschema.Schema {
	idx, err := strconv.Atoi(key)
	if err != nil || idx < 0 {
		return nil
	}
	if idx < len(s.PrefixItems) {
		return resolveSchemaRef(s.PrefixItems[idx])
	}
	if s.Items2020 != nil {
		return resolveSchemaRef(s.Items2020)
	}
	switch items := s.Items.(type) {
	case *jsonschema.Schema:
		return resolveSchemaRef(items)
	case []*jsonschema.Schema:
		if idx < len(items) {
			return resolveSchemaRef(items[idx])
		}
	}
	return nil
}

// convertToJSON converts YAML-parsed data to JSON-compatible types.
func convertToJSON(v interface{}) interface{} {
	switch v := v.(type) {
	case map[string]interface{}:
		m := make(map[string]interface{})
		for k, val := range v {
			m[k] = convertToJSON(val)
		}
		return m
	// U096-F03: yaml.v3 hands back exactly two shapes that are NOT already
	// JSON-native, and this function's whole reason to exist is to convert
	// them. Both used to fall through to `default` untouched, so the schema
	// validator saw a *jsonType* error (not a *jsonschema.ValidationError*)
	// for either — silently defeating classifyValidationError's errors.As
	// and reporting neither the offending key nor a usable message.
	case map[interface{}]interface{}:
		// A YAML mapping key that isn't a bare string (e.g. a bare `1:` or a
		// `true:`) decodes into this shape rather than map[string]interface{}.
		m := make(map[string]interface{}, len(v))
		for k, val := range v {
			m[fmt.Sprint(k)] = convertToJSON(val)
		}
		return m
	case time.Time:
		// An unquoted YAML timestamp (e.g. `2026-07-24`) decodes to time.Time,
		// which encoding/json — and this validator's schema.Validate — cannot
		// represent; JSON Schema's own "date"/"date-time" formats expect a
		// string. RFC3339 round-trips both a bare date and a full timestamp.
		return v.Format(time.RFC3339)
	case []interface{}:
		arr := make([]interface{}, len(v))
		for i, val := range v {
			arr[i] = convertToJSON(val)
		}
		return arr
	default:
		return v
	}
}

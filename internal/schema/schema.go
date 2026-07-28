// Package schema provides JSON Schema validation for config YAML files.
package schema

import (
	"bytes"
	"fmt"
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

// resolveSchemaRef follows a chain of $ref indirection to the schema that
// actually carries the object keywords (properties/additionalProperties/...).
func resolveSchemaRef(s *jsonschema.Schema) *jsonschema.Schema {
	for s != nil && s.Ref != nil {
		s = s.Ref
	}
	return s
}

// schemaChild returns the sub-schema key names within s, or nil if s does not
// recognize key at all. See KnownPath's doc for the resolution order.
func schemaChild(s *jsonschema.Schema, key string) *jsonschema.Schema {
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
	if sub, ok := s.AdditionalProperties.(*jsonschema.Schema); ok {
		return resolveSchemaRef(sub)
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

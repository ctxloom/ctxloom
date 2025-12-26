// Package schema provides JSON Schema validation for fragment YAML files.
package schema

import (
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"mlcm/resources"
)

// Validator validates fragment YAML against the JSON schema.
type Validator struct {
	schema *jsonschema.Schema
}

// NewValidator creates a new schema validator using the embedded schema.
func NewValidator() (*Validator, error) {
	// Load schema from embedded resources
	schemaData, err := resources.GetFragmentSchema()
	if err != nil {
		return nil, fmt.Errorf("failed to load schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("fragment.json", strings.NewReader(string(schemaData))); err != nil {
		return nil, fmt.Errorf("failed to add schema resource: %w", err)
	}

	schema, err := compiler.Compile("fragment.json")
	if err != nil {
		return nil, fmt.Errorf("failed to compile schema: %w", err)
	}

	return &Validator{schema: schema}, nil
}

// ValidateBytes validates YAML content against the fragment schema.
func (v *Validator) ValidateBytes(data []byte) error {
	var yamlData interface{}
	if err := yaml.Unmarshal(data, &yamlData); err != nil {
		return fmt.Errorf("YAML parse error: %w", err)
	}

	jsonData := convertToJSON(yamlData)
	return v.schema.Validate(jsonData)
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

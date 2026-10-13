package config

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// A SchemaViolation reads as the type it holds against the type expected
// when it is a type mismatch, and as the broken constraint otherwise; the
// document root, which has no dotted path, is named as the top level.
func TestSchemaViolation_Error(t *testing.T) {
	tests := map[string]struct {
		v    SchemaViolation
		want string
	}{
		"a type mismatch at a key": {
			SchemaViolation{File: "c.yaml", KeyPath: "agents.dev.profiles", Expected: "array", Got: "string"},
			"c.yaml: `agents.dev.profiles` holds a string, expected array",
		},
		"a broken constraint at a key": {
			SchemaViolation{File: "c.yaml", KeyPath: "llm.configs.x", Constraint: "value must be one of 'a', 'b'"},
			"c.yaml: `llm.configs.x` breaks the schema: value must be one of 'a', 'b'",
		},
		"a violation at the document root": {
			SchemaViolation{File: "c.yaml", Expected: "object", Got: "array"},
			"c.yaml: `(top level)` holds a array, expected object",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.v.Error())
		})
	}
}

// Every SchemaViolation matches ErrSchemaViolation, however it is worded, and
// nothing else.
func TestSchemaViolation_IsTheSentinel(t *testing.T) {
	var err error = &SchemaViolation{File: "c.yaml", Constraint: "anything"}
	assert.ErrorIs(t, err, ErrSchemaViolation)
	assert.NotErrorIs(t, err, errors.New("the config breaks its schema"))
}

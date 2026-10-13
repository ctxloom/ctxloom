package config

import (
	"errors"
	"fmt"
)

// ErrSchemaViolation is the sentinel every SchemaViolation matches
// (errors.Is), so a caller can recognize "a config file breaks its schema"
// without depending on how a particular violation is worded.
var ErrSchemaViolation = errors.New("the config breaks its schema")

// Renderings of a SchemaViolation. Constants, so the shape a user reads — and
// a test asserts — has one spelling.
const (
	// schemaViolationTypeFormat: file, key path, expected type, actual type.
	schemaViolationTypeFormat = "%s: `%s` holds a %s, expected %s"
	// schemaViolationConstraintFormat: file, key path, the broken constraint.
	schemaViolationConstraintFormat = "%s: `%s` breaks the schema: %s"
	// topLevelKeyPath names the document root, which has no dotted path.
	topLevelKeyPath = "(top level)"
)

// SchemaViolation is one place a config file breaks the config schema in a
// way other than carrying a key the schema does not know (that is
// WarnKindUnknownKey's case). It names the file, the offending key by its
// dotted path, and either the type the schema expected (Expected, with the
// type found in Got) or, for a violation that is not a type mismatch, the
// constraint the value broke (Constraint).
type SchemaViolation struct {
	File string
	// KeyPath is the dotted config path ("agents.dev.profiles"); empty for
	// the document root.
	KeyPath string
	// Expected and Got are the schema's type(s) and the value's type, set
	// only for a type mismatch.
	Expected string
	Got      string
	// Constraint is the validator's statement of the rule the value broke,
	// set when the violation is not a type mismatch.
	Constraint string
}

func (v *SchemaViolation) Error() string {
	key := v.KeyPath
	if key == "" {
		key = topLevelKeyPath
	}
	if v.Expected != "" {
		return fmt.Sprintf(schemaViolationTypeFormat, v.File, key, v.Got, v.Expected)
	}
	return fmt.Sprintf(schemaViolationConstraintFormat, v.File, key, v.Constraint)
}

// Is makes every SchemaViolation match ErrSchemaViolation.
func (v *SchemaViolation) Is(target error) bool { return target == ErrSchemaViolation }

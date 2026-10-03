package profiles

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
	"github.com/ctxloom/ctxloom/resources"
)

// profileValidator compiles the embedded profile schema once. The schema is
// the single owner of what a profile may say:
// TestArch_ProfileSchema_CoversEveryProfileField binds it to Profile's own yaml
// tags, so a field added to the struct without a schema entry fails that gate
// instead of becoming a key every load refuses.
var profileValidator = sync.OnceValues(func() (*schema.ConfigValidator, error) {
	data, err := resources.GetProfileSchema()
	if err != nil {
		return nil, fmt.Errorf("load profile schema: %w", err)
	}
	return schema.NewValidatorFromSchema(data)
})

// profileSchemaRemedy is the fix every schema-violation finding carries.
const profileSchemaRemedy = "correct the named keys and values in the profile, or delete it"

// validateProfileDocument checks a profile document against the declared
// profile schema and reports any violation as ONE fail-loudly config finding.
//
// It must run on the document BEFORE decoding: yaml.v3 drops what it cannot
// map and coerces what it can, so after Decode a typo'd key is gone, `llm: 5`
// is the string "5", and a bare `- ` list entry is an empty name. The schema
// sees the document as written.
//
// It reports and never decides: strictness owns whether the finding refuses
// the launch (strict) or only warns (--degraded), so there is no strict flag
// here and the finding is deliberately degradable.
//
// A document with no content at all is not validated: that is the
// empty-profile finding loadFile reports, and answering it twice in two
// vocabularies helps nobody. A schema that cannot be loaded is an error, not
// a pass — a broken check must not report every profile as valid.
func validateProfileDocument(rep report.Reporter, path string, doc *yaml.Node, data []byte) error {
	if doc.Kind == 0 {
		return nil
	}
	v, err := profileValidator()
	if err != nil {
		return err
	}
	verr := v.ValidateBytes(data)
	if verr == nil {
		return nil
	}
	rep.FailOncef(report.KindConfig, profileSchemaRemedy,
		"profile %s does not match the profile schema: %s", path, describeViolations(verr))
	return nil
}

// describeViolations renders a validation failure as its concrete causes, each
// at the document location it concerns.
//
// Where a value must match ONE of several shapes (a fragments entry is a name
// or a {name, priority} mapping), it fails every shape it is not; those
// alternatives are dropped when a cause outside them exists, because they
// describe how the schema chose a branch rather than a second defect. When
// every cause is an alternative, they are all the information there is, so
// all are kept.
func describeViolations(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return err.Error()
	}
	leaves := leafViolations(ve)
	var direct []*jsonschema.ValidationError
	for _, leaf := range leaves {
		if !inBranchAlternative(leaf) {
			direct = append(direct, leaf)
		}
	}
	if len(direct) > 0 {
		leaves = direct
	}
	parts := make([]string, 0, len(leaves))
	seen := make(map[string]bool, len(leaves))
	for _, leaf := range leaves {
		loc := leaf.InstanceLocation
		if loc == "" {
			loc = "/"
		}
		part := "`" + loc + "`: " + leaf.Message
		if !seen[part] {
			seen[part] = true
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "; ")
}

// leafViolations flattens the error tree to the causes that name a concrete
// violation rather than "does not validate with ...".
func leafViolations(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, leafViolations(c)...)
	}
	return out
}

// inBranchAlternative reports whether a cause comes from inside one
// alternative of a oneOf/anyOf, read off its keyword location (a JSON pointer
// into the schema).
func inBranchAlternative(leaf *jsonschema.ValidationError) bool {
	return strings.Contains(leaf.KeywordLocation, "/oneOf/") || strings.Contains(leaf.KeywordLocation, "/anyOf/")
}

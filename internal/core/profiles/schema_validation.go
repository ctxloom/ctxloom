package profiles

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/keymatch"
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

// errProfileSchema is a profile document that does not match the profile
// schema. Decode wraps it with each violation and where it is.
var errProfileSchema = errors.New("profile does not match the profile schema")

// validateProfileDocument checks a profile document against the declared
// profile schema, returning every violation as one error.
//
// It must run on the document BEFORE decoding: yaml.v3 drops what it cannot
// map and coerces what it can, so after Decode a typo'd key is gone, `llm: 5`
// is the string "5", and a bare `- ` list entry is an empty name. The schema
// sees the document as written.
//
// A document with no content at all is not validated: an empty profile is a
// question for the writers and the fail-loudly gate, not a schema defect. A
// schema that cannot be loaded is an error, not a pass — a broken check must
// not report every profile as valid.
func validateProfileDocument(doc *yaml.Node, data []byte) error {
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
	return fmt.Errorf("%w: %s; correct the named keys and values", errProfileSchema, describeViolations(verr, v, doc))
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
//
// An unknown-key cause also names the key the author most likely meant, read
// from the document as written (doc) against the keys the schema declares at
// that location (v).
func describeViolations(err error, v *schema.ConfigValidator, doc *yaml.Node) string {
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
		part := "`" + loc + "`: " + leaf.Message + nearKeys(leaf, v, doc)
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

// nearKeys renders a did-you-mean for each undeclared key of the mapping an
// additionalProperties violation concerns, or "" for any other violation.
//
// The undeclared keys come from the document, not the violation's message:
// the mapping at the violation's location minus the keys the schema declares
// there. keymatch.Nearest is the one calibration every unknown-key surface
// shares, so a profile suggests exactly what a config or a bundle would.
func nearKeys(leaf *jsonschema.ValidationError, v *schema.ConfigValidator, doc *yaml.Node) string {
	if !strings.HasSuffix(leaf.KeywordLocation, "/additionalProperties") {
		return ""
	}
	segs := pointerSegments(leaf.InstanceLocation)
	m := mappingAt(doc, segs)
	if m == nil {
		return ""
	}
	known := v.KnownKeys(segs)
	var b strings.Builder
	for i := 0; i+1 < len(m.Content); i += 2 {
		key := m.Content[i].Value
		if slices.Contains(known, key) {
			continue
		}
		if near := keymatch.Nearest(key, known); near != "" {
			fmt.Fprintf(&b, " — did you mean `%s` for `%s`?", near, key)
		}
	}
	return b.String()
}

// pointerSegments splits a JSON pointer (RFC 6901) into its unescaped
// segments; the root pointer "" is no segments.
func pointerSegments(ptr string) []string {
	if ptr == "" || ptr == "/" {
		return nil
	}
	segs := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	for i, s := range segs {
		segs[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return segs
}

// mappingAt walks doc along segs — mapping keys and sequence indices — and
// returns the mapping node it lands on, or nil when the path does not resolve
// to one.
func mappingAt(doc *yaml.Node, segs []string) *yaml.Node {
	n := doc
	if n != nil && n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	for _, seg := range segs {
		n = yamlChild(n, seg)
	}
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	return n
}

// yamlChild is n's value under one pointer segment: a mapping's value for that
// key, or a sequence's element at that index; nil when there is none.
func yamlChild(n *yaml.Node, seg string) *yaml.Node {
	if n == nil {
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			if n.Content[i].Value == seg {
				return n.Content[i+1]
			}
		}
	case yaml.SequenceNode:
		if idx, err := strconv.Atoi(seg); err == nil && idx >= 0 && idx < len(n.Content) {
			return n.Content[idx]
		}
	}
	return nil
}

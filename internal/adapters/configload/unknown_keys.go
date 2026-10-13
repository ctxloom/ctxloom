package configload

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/keymatch"
	"github.com/ctxloom/ctxloom/internal/shared/schema"
)

// The config schema is authored with additionalProperties:false at every level,
// so an unknown key IS already detected on load — but jsonschema reports it as
//
//	jsonschema: '/profiles' does not validate with
//	https://ctxloom.dev/schemas/config.json#/properties/profiles/additionalProperties:
//	additionalProperties 'defaults' not allowed
//
// which tells a user nothing about what to write instead. This file translates
// those violations into the diagnostic the fail-loudly model promises: the
// offending key by its DOTTED PATH, the near-miss key it probably meant and
// the keys the section does know.

// additionalPropsRe extracts the offending key names from a jsonschema
// additionalProperties violation ("additionalProperties 'a', 'b' not allowed").
var additionalPropsRe = regexp.MustCompile(`^additionalProperties (.+) not allowed$`)

// branchAlternativeRe matches a keyword location that passes through ONE
// alternative of an anyOf/oneOf — "/anyOf/3/", "/oneOf/0/" and so on.
var branchAlternativeRe = regexp.MustCompile(`/(anyOf|oneOf)/[0-9]+/`)

// fromBranchAlternative reports whether a leaf failure comes from inside a
// rejected anyOf/oneOf alternative. A document that must match ONE of several
// shapes (an llm.configs.<label> entry, one shape per backend) fails EVERY
// alternative it is not, so those failures describe the shapes the value is
// not — they are how the schema chose a branch, not a second defect in the
// document.
func fromBranchAlternative(leaf *jsonschema.ValidationError) bool {
	return branchAlternativeRe.MatchString(leaf.KeywordLocation)
}

// typeMismatchRe parses a jsonschema `type` violation ("expected string, but
// got number"; a multi-type schema joins its types with " or ").
var typeMismatchRe = regexp.MustCompile(`^expected (.+), but got (.+)$`)

// branchPointRe matches the keyword location of an anyOf/oneOf node itself
// (".../anyOf"), as opposed to one of its alternatives (".../anyOf/3/...").
var branchPointRe = regexp.MustCompile(`/(anyOf|oneOf)$`)

// noAlternativeMatched is the constraint reported for an entry that matches
// none of the shapes an anyOf/oneOf allows: the per-alternative failures
// describe the shapes it is NOT, so none of them is the defect on its own.
const noAlternativeMatched = "matches none of the shapes the schema allows here"

// typeKeyword ends the keyword location of a type mismatch; constKeyword and
// enumKeyword those of the value checks a branching schema discriminates on;
// discriminatorKey ends the instance location of the property it
// discriminates by (an llm.configs.<label> entry's backend is its `type`).
const (
	typeKeyword      = "/type"
	constKeyword     = "/const"
	enumKeyword      = "/enum"
	discriminatorKey = "/type"
)

// classifyValidationError turns a schema validation failure into load warnings:
// one config.WarnKindUnknownKey per unknown key (named, suggested, de-retired),
// plus one config.WarnKindSchemaViolation per key that breaks the schema some
// other way (a wrong type, a bad enum), carrying a typed *config.SchemaViolation
// that names the file, the dotted key path and the expected type.
//
// Both of those counts are per DEFECT, not per schema leaf, and a branching
// schema does not report them one-to-one. An object behind an anyOf is
// validated against every alternative, so ONE unknown key inside an
// llm.configs.<label> entry produces one additionalProperties failure PER
// backend branch — seven identical warnings for one typo — and the branches
// whose discriminator did not match contribute a `const` failure each, which
// used to raise the "something else is also wrong" flag and append a raw
// jsonschema dump blaming a `type:` the user got RIGHT. Hence the rules
// below: unknown keys are deduplicated by (instance location, key), violations
// by key path, and a leaf from a rejected alternative does not count as a
// defect of its own.
//
// Suppressing branch failures cannot hide a real one: a document whose ONLY
// problem is inside a branch — an entry matching none of the alternatives —
// produces no other warning, and is reported as one violation at the entry
// the anyOf governs.
func classifyValidationError(configPath string, validator *schema.ConfigValidator, err error) []config.Warning {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []config.Warning{schemaViolationWarning(&config.SchemaViolation{File: configPath, Constraint: err.Error()})}
	}

	r := unknownKeyReport{configPath: configPath, validator: validator, reported: map[string]bool{}}
	v := violationReport{reported: map[string]bool{}}
	for _, leaf := range leafCauses(ve) {
		if r.add(leaf) {
			v.add(leaf.InstanceLocation, leafViolation(configPath, leaf))
		}
	}
	if len(r.warnings) == 0 && len(v.warnings) == 0 {
		for _, node := range branchPoints(ve) {
			v.addBranch(configPath, node)
		}
	}
	return append(r.warnings, v.warnings...)
}

// addBranch reports an anyOf/oneOf entry that matched none of its
// alternatives. When exactly one alternative's discriminator held (no failure
// at its `type` — the entry IS that shape, with a bad value inside), that
// alternative's own failures are the defects, named at their own keys.
// Otherwise the entry is the defect, reported once where the anyOf governs.
func (v *violationReport) addBranch(configPath string, node *jsonschema.ValidationError) {
	if alt := soleMatchingAlternative(node); alt != nil {
		for _, leaf := range leafCauses(alt) {
			if len(unknownKeysIn(leaf.Message)) == 0 {
				v.add(leaf.InstanceLocation, leafViolation(configPath, leaf))
			}
		}
		if len(v.warnings) > 0 {
			return
		}
	}
	v.add(node.InstanceLocation, &config.SchemaViolation{
		File: configPath, KeyPath: dottedPath(node.InstanceLocation), Constraint: noAlternativeMatched,
	})
}

// soleMatchingAlternative returns the one alternative of a branch node whose
// failures include no discriminator failure, or nil when there is not exactly
// one.
func soleMatchingAlternative(node *jsonschema.ValidationError) *jsonschema.ValidationError {
	var match *jsonschema.ValidationError
	for _, alt := range node.Causes {
		if slices.ContainsFunc(leafCauses(alt), isDiscriminatorFailure) {
			continue
		}
		if match != nil {
			return nil
		}
		match = alt
	}
	return match
}

// isDiscriminatorFailure reports whether a leaf rejects the entry's
// discriminating property (a `const` or `enum` failure at its `type`) — how a
// branching schema tells its alternatives apart. A const/enum failure on any
// other property is a bad value inside the shape the entry IS.
func isDiscriminatorFailure(leaf *jsonschema.ValidationError) bool {
	valueCheck := strings.HasSuffix(leaf.KeywordLocation, constKeyword) || strings.HasSuffix(leaf.KeywordLocation, enumKeyword)
	return valueCheck && strings.HasSuffix(leaf.InstanceLocation, discriminatorKey)
}

// violationReport accumulates one schema-violation warning per key path: a
// value that fails two keywords (a type and an enum) is one defect.
type violationReport struct {
	reported map[string]bool
	warnings []config.Warning
}

func (v *violationReport) add(instanceLocation string, violation *config.SchemaViolation) {
	if v.reported[instanceLocation] {
		return
	}
	v.reported[instanceLocation] = true
	v.warnings = append(v.warnings, schemaViolationWarning(violation))
}

// schemaViolationWarning wraps a violation as the warning that carries it.
func schemaViolationWarning(v *config.SchemaViolation) config.Warning {
	return config.Warning{Kind: config.WarnKindSchemaViolation, Text: v.Error(), Cause: v}
}

// leafViolation renders one non-unknown-key leaf as a typed violation: a type
// mismatch names the expected and actual types, anything else the constraint
// the validator states.
func leafViolation(configPath string, leaf *jsonschema.ValidationError) *config.SchemaViolation {
	out := &config.SchemaViolation{File: configPath, KeyPath: dottedPath(leaf.InstanceLocation)}
	if m := typeMismatchRe.FindStringSubmatch(leaf.Message); m != nil && strings.HasSuffix(leaf.KeywordLocation, typeKeyword) {
		out.Expected, out.Got = m[1], m[2]
		return out
	}
	out.Constraint = leaf.Message
	return out
}

// branchPoints returns the outermost anyOf/oneOf nodes in the error tree —
// the entries that matched none of their alternatives.
func branchPoints(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if branchPointRe.MatchString(ve.KeywordLocation) {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, branchPoints(c)...)
	}
	return out
}

// unknownKeyReport accumulates one unknown-key warning per (location, key).
type unknownKeyReport struct {
	configPath string
	validator  *schema.ConfigValidator
	reported   map[string]bool
	warnings   []config.Warning
}

// add records the unknown keys a leaf cause names, reporting whether the
// leaf is some OTHER failure: it names no unknown key and is not merely one
// anyOf alternative's complaint.
func (r *unknownKeyReport) add(leaf *jsonschema.ValidationError) (other bool) {
	keys := unknownKeysIn(leaf.Message)
	if len(keys) == 0 {
		return !fromBranchAlternative(leaf)
	}
	for _, key := range keys {
		if fromBranchAlternative(leaf) && keyKnownAt(r.validator, leaf.InstanceLocation, key) {
			// The key IS declared somewhere in this anyOf; only the
			// alternatives that do not declare it are complaining. Naming
			// it "unknown" would report a correct line as a typo — and
			// then helpfully suggest the key the user already wrote.
			continue
		}
		id := leaf.InstanceLocation + "\x00" + key
		if r.reported[id] {
			continue
		}
		r.reported[id] = true
		r.warnings = append(r.warnings, config.Warning{
			Kind: config.WarnKindUnknownKey,
			Text: unknownKeyMessage(r.configPath, leaf.InstanceLocation, key, r.validator),
		})
	}
	return false
}

// keyKnownAt reports whether key is a property the schema declares at
// instanceLocation, unioned across every branch. Used to tell a genuine
// unknown key apart from one that is legitimate in the branch the document
// actually matched and merely absent from the alternatives it did not.
func keyKnownAt(validator *schema.ConfigValidator, instanceLocation, key string) bool {
	return slices.Contains(validator.KnownKeys(sectionSegments(dottedPath(instanceLocation))), key)
}

// sectionSegments splits a dotted config section into the path segments
// KnownKeys/KnownPath resolve against. The empty section is the top level,
// which is a nil path rather than one empty segment.
func sectionSegments(section string) []string {
	if section == "" {
		return nil
	}
	return strings.Split(section, ".")
}

// leafCauses flattens the jsonschema error tree to its leaves — the causes that
// name a concrete violation rather than "does not validate with ...".
func leafCauses(ve *jsonschema.ValidationError) []*jsonschema.ValidationError {
	if len(ve.Causes) == 0 {
		return []*jsonschema.ValidationError{ve}
	}
	var out []*jsonschema.ValidationError
	for _, c := range ve.Causes {
		out = append(out, leafCauses(c)...)
	}
	return out
}

// unknownKeysIn parses the key names out of an additionalProperties violation
// message, returning nil for any other kind of violation.
func unknownKeysIn(message string) []string {
	m := additionalPropsRe.FindStringSubmatch(message)
	if m == nil {
		return nil
	}
	var keys []string
	for _, raw := range strings.Split(m[1], ",") {
		if key := strings.Trim(strings.TrimSpace(raw), "'"); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// unknownKeyMessage renders the user-visible line for one unknown key: the
// dotted path, a did-you-mean and the section's known keys.
func unknownKeyMessage(configPath, instanceLocation, key string, validator *schema.ConfigValidator) string {
	section := dottedPath(instanceLocation)
	path := key
	if section != "" {
		path = section + "." + key
	}

	var b strings.Builder
	fmt.Fprintf(&b, "unknown key `%s` in %s: ctxloom does not know it", path, configPath)

	// This used to re-walk the RAW schema JSON by hand
	// (configSchemaDocument/knownKeysAt) and get it wrong the moment the
	// violated object sat behind an anyOf/oneOf/allOf branch (e.g. any
	// llm.configs.<label> entry) — the raw walker expected a map at every
	// segment and an anyOf node is a list, so it silently returned nil and
	// every backend-specific typo lost its did-you-mean. internal/shared/schema's
	// compiled ConfigValidator already solves exactly this (KnownPath uses
	// the same schemaChild walk); KnownKeys is its enumeration counterpart,
	// unioning across every branch instead of stopping at the first match.
	known := validator.KnownKeys(sectionSegments(section))
	if suggestion := keymatch.Nearest(key, known); suggestion != "" {
		fmt.Fprintf(&b, " — did you mean `%s`?", suggestion)
	}
	if len(known) > 0 {
		where := "the top level"
		if section != "" {
			where = "`" + section + "`"
		}
		fmt.Fprintf(&b, " (known keys at %s: %s)", where, strings.Join(known, ", "))
	}
	return b.String()
}

// dottedPath converts a jsonschema instance location ("/profiles/definitions/dev")
// into the dotted config path a user recognizes ("profiles.definitions.dev").
func dottedPath(instanceLocation string) string {
	trimmed := strings.Trim(instanceLocation, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	for i, p := range parts {
		// JSON-pointer escapes (RFC 6901); ~1 must be decoded before ~0.
		p = strings.ReplaceAll(p, "~1", "/")
		parts[i] = strings.ReplaceAll(p, "~0", "~")
	}
	return strings.Join(parts, ".")
}

// The did-you-mean calibration this file used to carry (nearestKey +
// editDistance) now lives in internal/shared/keymatch, because the bundle
// loader's strict YAML decode asks the same question of the same kind of
// mistake and the two answers must be identical.

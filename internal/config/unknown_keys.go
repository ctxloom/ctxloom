package config

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/ctxloom/ctxloom/internal/schema"
	"github.com/ctxloom/ctxloom/internal/shared/keymatch"
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
// offending key by its DOTTED PATH, the fact that ctxloom ignored it, the
// near-miss key it probably meant, and — for a key a past schema generation
// RETIRED — the key that replaced it.
//
// Ordering matters and is owned by loadConfigFile: validation (and therefore
// this classification) runs AFTER the upgrade pipeline, so a key an older config
// still legitimately carries is migrated forward first and never reaches here.
// Only a key that survives migration — i.e. one the current schema truly does
// not know — is reported.

// retiredKeys maps a dotted config path that a schema generation RETIRED to the
// guidance that names its replacement. These fire only when the migrator did NOT
// rewrite the key — i.e. the document already claims a version at or above the
// migration that would have moved it, which is exactly the "copied a stale doc
// into a current config" case.
var retiredKeys = map[string]string{
	"profiles.defaults": "`profiles.defaults` was RETIRED: the default context is now whatever the default AGENT composes — " +
		"set `default_agent: <name>` and `agents.<name>.profiles: [...]`",
	"defaults":       "the top-level `defaults` bag was RETIRED: use `llm.defaults.primary` / `llm.defaults.fast` for models, and `default_agent` for the default context",
	"llm.plugins":    "`llm.plugins` was RENAMED to `llm.configs`",
	"llm.default":    "`llm.default` was REPLACED by `llm.defaults.primary`",
	"llm.compaction": "`llm.compaction` was REPLACED by `llm.defaults.fast`",
	"subagents":      "`subagents` was RENAMED to `agents`",
}

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

// classifyValidationError turns a schema validation failure into load warnings:
// one WarnKindUnknownKey per unknown key (named, suggested, de-retired), plus a
// single WarnKindValidate carrying the raw error when the document ALSO breaks
// the schema in some other way (a bad enum, a wrong type). A failure with no
// recognizable unknown-key cause degrades to today's behavior verbatim: one
// WarnKindValidate with the original text.
//
// Both of those counts are per DEFECT, not per schema leaf, and a branching
// schema does not report them one-to-one. An object behind an anyOf is
// validated against every alternative, so ONE unknown key inside an
// llm.configs.<label> entry produces one additionalProperties failure PER
// backend branch — seven identical warnings for one typo — and the branches
// whose discriminator did not match contribute a `const` failure each, which
// used to raise the "something else is also wrong" flag and append a raw
// jsonschema dump blaming a `type:` the user got RIGHT. Hence the two rules
// below: unknown keys are deduplicated by (instance location, key), and a
// leaf from a rejected alternative does not count as a second defect.
//
// Suppressing branch failures cannot hide a real one: a document whose ONLY
// problem is inside a branch produces no unknown-key warnings at all, and the
// empty-warnings case still emits the raw error verbatim.
func classifyValidationError(configPath string, validator *schema.ConfigValidator, err error) []Warning {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Warning{{Kind: WarnKindValidate, Text: fmt.Sprintf("config validation warning at %s: %v", configPath, err)}}
	}

	var warnings []Warning
	var other bool
	reported := map[string]bool{}
	for _, leaf := range leafCauses(ve) {
		keys := unknownKeysIn(leaf.Message)
		if len(keys) == 0 {
			if !fromBranchAlternative(leaf) {
				other = true
			}
			continue
		}
		for _, key := range keys {
			if fromBranchAlternative(leaf) && keyKnownAt(validator, leaf.InstanceLocation, key) {
				// The key IS declared somewhere in this anyOf; only the
				// alternatives that do not declare it are complaining. Naming
				// it "unknown" would report a correct line as a typo — and
				// then helpfully suggest the key the user already wrote.
				continue
			}
			id := leaf.InstanceLocation + "\x00" + key
			if reported[id] {
				continue
			}
			reported[id] = true
			warnings = append(warnings, Warning{
				Kind: WarnKindUnknownKey,
				Text: unknownKeyMessage(configPath, leaf.InstanceLocation, key, validator),
			})
		}
	}
	if len(warnings) == 0 || other {
		warnings = append(warnings, Warning{Kind: WarnKindValidate, Text: fmt.Sprintf("config validation warning at %s: %v", configPath, err)})
	}
	return warnings
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
// dotted path, the fact that it was ignored, the retired-key replacement when we
// know one, and otherwise a did-you-mean plus the section's known keys.
func unknownKeyMessage(configPath, instanceLocation, key string, validator *schema.ConfigValidator) string {
	section := dottedPath(instanceLocation)
	path := key
	if section != "" {
		path = section + "." + key
	}

	var b strings.Builder
	fmt.Fprintf(&b, "unknown key `%s` in %s: ctxloom does not know it, so it is IGNORED", path, configPath)
	if hint, ok := retiredKeys[path]; ok {
		fmt.Fprintf(&b, " — %s", hint)
		return b.String()
	}

	// This used to re-walk the RAW schema JSON by hand
	// (configSchemaDocument/knownKeysAt) and get it wrong the moment the
	// violated object sat behind an anyOf/oneOf/allOf branch (e.g. any
	// llm.configs.<label> entry) — the raw walker expected a map at every
	// segment and an anyOf node is a list, so it silently returned nil and
	// every backend-specific typo lost its did-you-mean. internal/schema's
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

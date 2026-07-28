package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v5"

	"github.com/ctxloom/ctxloom/internal/schema"
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

// classifyValidationError turns a schema validation failure into load warnings:
// one WarnKindUnknownKey per unknown key (named, suggested, de-retired), plus a
// single WarnKindValidate carrying the raw error when the document ALSO breaks
// the schema in some other way (a bad enum, a wrong type). A failure with no
// recognizable unknown-key cause degrades to today's behavior verbatim: one
// WarnKindValidate with the original text.
func classifyValidationError(configPath string, validator *schema.ConfigValidator, err error) []Warning {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return []Warning{{Kind: WarnKindValidate, Text: fmt.Sprintf("config validation warning at %s: %v", configPath, err)}}
	}

	var warnings []Warning
	var other bool
	for _, leaf := range leafCauses(ve) {
		keys := unknownKeysIn(leaf.Message)
		if len(keys) == 0 {
			other = true
			continue
		}
		for _, key := range keys {
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

	// U096-F02: this used to re-walk the RAW schema JSON by hand
	// (configSchemaDocument/knownKeysAt) and get it wrong the moment the
	// violated object sat behind an anyOf/oneOf/allOf branch (e.g. any
	// llm.configs.<label> entry) — the raw walker expected a map at every
	// segment and an anyOf node is a list, so it silently returned nil and
	// every backend-specific typo lost its did-you-mean. internal/schema's
	// compiled ConfigValidator already solves exactly this (KnownPath uses
	// the same schemaChild walk); KnownKeys is its enumeration counterpart,
	// unioning across every branch instead of stopping at the first match.
	var segments []string
	if section != "" {
		segments = strings.Split(section, ".")
	}
	known := validator.KnownKeys(segments)
	if suggestion := nearestKey(key, known); suggestion != "" {
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

// nearestKey returns the known key closest to the offending one, when it is
// close enough to be a plausible typo (edit distance ≤ 2, and never more than a
// third of the key's length — so `sync` doesn't "suggest" `ui`). Empty when
// nothing is near.
func nearestKey(key string, known []string) string {
	best, bestDist := "", 1<<30
	budget := len(key) / 3
	if budget > 2 {
		budget = 2
	}
	if budget < 1 {
		budget = 1
	}
	for _, k := range known {
		if d := editDistance(key, k); d < bestDist {
			best, bestDist = k, d
		}
	}
	if bestDist > budget {
		return ""
	}
	return best
}

// editDistance is the Levenshtein distance between a and b (two-row DP).
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	curr := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(br)]
}

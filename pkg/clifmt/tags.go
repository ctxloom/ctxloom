package clifmt

import (
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

var (
	camelLowerUpper = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	camelAcronymRun = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

// parseJSONTag reads a field's `json:"..."` tag, returning the identity name
// (empty if absent), whether the field is excluded (json:"-"), and whether
// omitempty was requested. This is clifmt's sole source of field identity
// for the reflective text/markdown renderer, per the package's tag
// convention: json tags name and gate fields, label/col only tune display.
func parseJSONTag(tag reflect.StructTag) (name string, skip bool, omitempty bool) {
	raw, ok := tag.Lookup("json")
	if !ok || raw == "" {
		return "", false, false
	}
	parts := strings.Split(raw, ",")
	if parts[0] == "-" && len(parts) == 1 {
		return "", true, false
	}
	name = parts[0]
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, false, omitempty
}

// resolveLabel returns the human heading/line label for a field: an explicit
// `label:"..."` tag wins, otherwise the json-tag-derived name (or the Go
// field name if untagged) is humanized.
func resolveLabel(field reflect.StructField, jsonName string) string {
	if l := field.Tag.Get("label"); l != "" {
		return l
	}
	base := jsonName
	if base == "" {
		base = field.Name
	}
	return humanize(base)
}

// resolveCol returns the table column header for a field: an explicit
// `col:"..."` tag wins, otherwise it falls back to the field's label.
func resolveCol(field reflect.StructField, label string) string {
	if c := field.Tag.Get("col"); c != "" {
		return c
	}
	return label
}

// humanize turns a snake_case or CamelCase identifier into a spaced, title
// cased heading (e.g. "created_at" / "CreatedAt" -> "Created At"), preserving
// runs of uppercase letters (acronyms like "ID" or "HTTP") as-is rather than
// forcing them to a single leading capital.
func humanize(s string) string {
	if s == "" {
		return ""
	}
	s = strings.NewReplacer("_", " ", "-", " ").Replace(s)
	s = camelAcronymRun.ReplaceAllString(s, "$1 $2")
	s = camelLowerUpper.ReplaceAllString(s, "$1 $2")

	words := strings.Fields(s)
	for i, w := range words {
		if isAllUpper(w) {
			continue
		}
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		words[i] = string(r)
	}
	return strings.Join(words, " ")
}

func isAllUpper(s string) bool {
	hasLetter := false
	for _, r := range s {
		if unicode.IsLetter(r) {
			hasLetter = true
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return hasLetter
}

// isEmptyValue mirrors encoding/json's own omitempty definition: booleans,
// numbers, strings, and container types are empty at their zero value or
// zero length; pointers/interfaces are empty when nil. Struct values are
// never considered empty, matching encoding/json's documented behavior, so
// omitempty never hides a nested-section field.
func isEmptyValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	default:
		return false
	}
}

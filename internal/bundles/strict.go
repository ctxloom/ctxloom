package bundles

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/keymatch"
)

// This file is the diagnostic half of ParseBundle's strict decode.
//
// ParseBundle reads bundle YAML with KnownFields(true), so a key the Bundle
// schema does not model is an ERROR instead of a silent drop. That refusal is
// the whole point: a misspelled `hoooks:` or a `promt:` used to load cleanly
// and leave the bundle quietly doing less than its author wrote — exit 0, a
// success message, and a hook that never fires. detectLegacySkillsKey exists
// because one such misparse already cost someone a debugging session.
//
// yaml's own complaint for that case is
//
//	yaml: unmarshal errors:
//	  line 12: field hoooks not found in type bundles.Bundle
//
// which names the key and a Go type nobody authoring a bundle has heard of,
// and says nothing about what to write instead. strictDecodeError rewrites it
// into the offending key, where in the document that key would have been
// legal, the nearest key it plausibly meant, and the full set of keys valid
// there. The FILE is supplied by the caller — every ParseBundle call site
// wraps with its own source identity (localFSReader.readBundle names the path,
// readEnvelope names the tree bundle, companionReader.read names the companion
// binary) — because ParseBundle takes bytes and has never known where they
// came from.

// unknownFieldRe matches one gopkg.in/yaml.v3 KnownFields complaint. yaml
// emits exactly this shape per offending key, with the Go type it was
// decoding into; anything else in a TypeError is a different defect (a type
// mismatch, say) and passes through untouched.
var unknownFieldRe = regexp.MustCompile(`^line (\d+): field (\S+) not found in type (\S+)$`)

// strictDecodeError turns a decode failure into the message a bundle author
// can act on, rewriting every "field X not found" complaint and leaving any
// other complaint verbatim. A non-TypeError (malformed YAML) passes straight
// through: yaml's own syntax errors already point at a line.
func strictDecodeError(err error) error {
	var te *yaml.TypeError
	if !errors.As(err, &te) {
		return fmt.Errorf("invalid bundle YAML: %w", err)
	}
	sites := bundleKeySites()
	rendered := make([]string, 0, len(te.Errors))
	for _, e := range te.Errors {
		m := unknownFieldRe.FindStringSubmatch(e)
		if m == nil {
			rendered = append(rendered, e)
			continue
		}
		rendered = append(rendered, unknownBundleKeyMessage(m[1], m[2], sites[m[3]]))
	}
	return fmt.Errorf("invalid bundle YAML: %s", strings.Join(rendered, "; "))
}

// unknownBundleKeyMessage renders one refused key: what it was, where it sat,
// what it probably should have been, and everything that is legal there.
func unknownBundleKeyMessage(line, key string, site keySite) string {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown key `%s` on line %s", key, line)
	if site.where != "" {
		fmt.Fprintf(&b, " (at %s)", site.where)
	}
	b.WriteString(": a bundle may not declare a key ctxloom does not model — it would be dropped in silence and the bundle would quietly ship less than it declares")
	if suggestion := keymatch.Nearest(key, site.keys); suggestion != "" {
		fmt.Fprintf(&b, " — did you mean `%s`?", suggestion)
	}
	if len(site.keys) > 0 {
		fmt.Fprintf(&b, " (valid keys there: %s)", strings.Join(site.keys, ", "))
	}
	return b.String()
}

// keySite is one place in a bundle document that has its own vocabulary: a
// human phrase for where it is, and the keys that are legal there.
type keySite struct {
	where string
	keys  []string
}

var (
	keySitesOnce sync.Once
	keySitesMap  map[string]keySite
)

// bundleKeySites maps the Go type name yaml reports ("bundles.Bundle",
// "bundles.BundleFragment", "profiles.Profile") to the vocabulary legal at
// that point in a bundle document.
//
// It is DERIVED from the structs by reflection rather than written out,
// because a hand-maintained list of valid keys is one field addition away from
// telling an author their correct key is invalid — the exact failure mode this
// whole change exists to prevent, relocated into the error message.
func bundleKeySites() map[string]keySite {
	keySitesOnce.Do(func() {
		keySitesMap = map[string]keySite{}
		collectKeySites(reflect.TypeOf(Bundle{}), "", keySitesMap)
	})
	return keySitesMap
}

// collectKeySites walks the struct graph reachable from t, recording each
// struct type's yaml keys against the document path it is reached by. The
// first path that reaches a type wins (`fragments.<name>` for BundleFragment),
// and a type already recorded is not revisited, which is also what terminates
// the walk on a recursive schema.
func collectKeySites(t reflect.Type, path string, out map[string]keySite) {
	for t.Kind() == reflect.Pointer || t.Kind() == reflect.Slice || t.Kind() == reflect.Array || t.Kind() == reflect.Map {
		switch t.Kind() {
		case reflect.Map:
			path += ".<name>"
		case reflect.Slice, reflect.Array:
			path += "[]"
		}
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	if _, seen := out[t.String()]; seen {
		return
	}
	// Claim the slot before recursing so a self-referential type stops here.
	out[t.String()] = keySite{where: describePath(path)}

	var keys []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported: not decodable, so never a valid key
		}
		name := yamlFieldName(f)
		if name == "" {
			continue // yaml:"-": deliberately not settable from a document
		}
		keys = append(keys, name)
		child := name
		if path != "" {
			child = path + "." + name
		}
		collectKeySites(f.Type, child, out)
	}
	sort.Strings(keys)
	out[t.String()] = keySite{where: describePath(path), keys: keys}
}

// yamlFieldName is the key a struct field decodes from: its yaml tag name, or
// the lowercased field name when it carries no tag (yaml's own default).
// Empty means the field is not addressable from a document at all.
func yamlFieldName(f reflect.StructField) string {
	tag, ok := f.Tag.Lookup("yaml")
	if !ok {
		return strings.ToLower(f.Name)
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return ""
	}
	if name == "" {
		return strings.ToLower(f.Name)
	}
	return name
}

// describePath renders a document path as the phrase an error message uses.
func describePath(path string) string {
	if path == "" {
		return "the top level of the bundle"
	}
	return "`" + path + "`"
}

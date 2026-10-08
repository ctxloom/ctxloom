package clifmt

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// hint is one field's parsed `clifmt:"…"` struct tag: the display hints
// that tune the derived human views (text and markdown). json, yaml and toml
// never read it; the json tag alone sets the machine shape.
//
// Grammar: `clifmt:"-"`, or a comma-separated list of key=value pairs:
//
//	label=…   heading or line label (default: the humanized json name)
//	col=…     table column header (default: the label)
//	role=…    semantic role: id, primary, status or detail. The grammar is
//	          reserved; no view acts on a role yet.
//	-         hide the field from the derived human views only
//
// A value may not contain ',' or '='. A label that needs either takes a
// custom view instead. A malformed tag is an error, never ignored.
type hint struct {
	label string
	col   string
	role  string
	hide  bool
}

// hintTagKey is the one struct-tag key clifmt reads.
const hintTagKey = "clifmt"

var validRoles = map[string]bool{"id": true, "primary": true, "status": true, "detail": true}

// parseHint parses the raw value of a clifmt tag.
func parseHint(raw string) (hint, error) {
	switch raw {
	case "":
		return hint{}, nil
	case "-":
		return hint{hide: true}, nil
	}
	var h hint
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		key, val, err := splitHintPart(part)
		if err != nil {
			return hint{}, err
		}
		if seen[key] {
			return hint{}, fmt.Errorf("key %q given twice", key)
		}
		seen[key] = true
		if err := h.set(key, val); err != nil {
			return hint{}, err
		}
	}
	return h, nil
}

// splitHintPart splits one comma-separated element of a clifmt tag into its
// key and non-empty value.
func splitHintPart(part string) (key, val string, err error) {
	if part == "-" {
		return "", "", fmt.Errorf(`"-" must stand alone`)
	}
	key, val, ok := strings.Cut(part, "=")
	if !ok || strings.Contains(val, "=") {
		return "", "", fmt.Errorf("%q is not key=value", part)
	}
	if val == "" {
		return "", "", fmt.Errorf("key %q has an empty value", key)
	}
	return key, val, nil
}

// set records one key=value pair.
func (h *hint) set(key, val string) error {
	switch key {
	case "label":
		h.label = val
	case "col":
		h.col = val
	case "role":
		if !validRoles[val] {
			return fmt.Errorf("unknown role %q (want id, primary, status or detail)", val)
		}
		h.role = val
	default:
		return fmt.Errorf("unknown key %q (want label, col or role)", key)
	}
	return nil
}

// typeHints is the parsed hints of every visible field of one struct type,
// keyed by the field's index path, or the first parse error among them.
type typeHints struct {
	byIndex map[string]hint
	err     error
}

// hintCache holds one typeHints per struct type: a type's tags never change,
// so each is parsed once per process.
var hintCache sync.Map // reflect.Type -> *typeHints

// hintsFor returns the parsed hints of struct type t, parsing and caching
// them on first use. A malformed tag on ANY visible field fails the type, so
// the error does not depend on which fields a given value happens to show.
func hintsFor(t reflect.Type) (*typeHints, error) {
	if cached, ok := hintCache.Load(t); ok {
		th := cached.(*typeHints)
		return th, th.err
	}
	th := &typeHints{byIndex: map[string]hint{}}
	for _, sf := range reflect.VisibleFields(t) {
		raw, ok := sf.Tag.Lookup(hintTagKey)
		if !ok {
			continue
		}
		h, err := parseHint(raw)
		if err != nil {
			th.err = fmt.Errorf("clifmt: %s.%s: bad %s tag %q: %w", t, sf.Name, hintTagKey, raw, err)
			break
		}
		th.byIndex[indexKey(sf.Index)] = h
	}
	actual, _ := hintCache.LoadOrStore(t, th)
	th = actual.(*typeHints)
	return th, th.err
}

// of returns the hint for the visible field sf.
func (th *typeHints) of(sf reflect.StructField) hint {
	return th.byIndex[indexKey(sf.Index)]
}

func indexKey(index []int) string {
	return fmt.Sprint(index)
}

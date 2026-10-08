package clifmt

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// validatePaths checks every At path against t, the static type of the value
// being rendered. Checking the type rather than the value is what keeps an
// omitempty field that happens to be absent from reading as a typo.
func validatePaths(t reflect.Type, paths map[string]viewFn) error {
	keys := make([]string, 0, len(paths))
	for p := range paths {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	var errs []error
	for _, p := range keys {
		if err := validatePath(t, p); err != nil {
			errs = append(errs, fmt.Errorf("clifmt: At(%q): %w", p, err))
		}
	}
	return errors.Join(errs...)
}

// validatePath walks path's segments down t. Below an interface the type is
// only known at run time, so the rest of the path is not checked.
func validatePath(t reflect.Type, path string) error {
	if path == "" {
		return nil
	}
	if t == nil {
		return errors.New("the value is nil, so the path addresses nothing")
	}
	segs, err := splitPath(path)
	if err != nil {
		return err
	}
	for _, seg := range segs {
		t = derefType(t)
		if t.Kind() == reflect.Interface {
			return nil
		}
		if t, err = stepPath(t, seg); err != nil {
			return err
		}
	}
	return nil
}

// stepPath returns the type one path segment below t.
func stepPath(t reflect.Type, seg string) (reflect.Type, error) {
	if seg == "[]" {
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return nil, fmt.Errorf("%s is not a list, so [] addresses nothing", t)
		}
		return t.Elem(), nil
	}
	if t.Kind() != reflect.Struct || typeImplementsStringer(t) {
		return nil, fmt.Errorf("%s renders as one value; it has no field %q", t, seg)
	}
	sf, err := humanFieldNamed(t, seg)
	if err != nil {
		return nil, err
	}
	return sf.Type, nil
}

// humanFieldNamed finds the field of struct t whose path segment is name.
func humanFieldNamed(t reflect.Type, name string) (reflect.StructField, error) {
	hints, err := hintsFor(t)
	if err != nil {
		return reflect.StructField{}, err
	}
	for _, sf := range reflect.VisibleFields(t) {
		if !sf.IsExported() {
			continue
		}
		jsonName, skip, _ := parseJSONTag(sf.Tag)
		if jsonName == "" {
			jsonName = sf.Name
		}
		if skip || jsonName != name {
			continue
		}
		if hints.of(sf).hide {
			return reflect.StructField{}, fmt.Errorf("%s.%s is hidden by clifmt:\"-\", so no view of it ever renders", t, sf.Name)
		}
		return sf, nil
	}
	return reflect.StructField{}, fmt.Errorf("%s has no field %q", t, name)
}

// splitPath splits a dotted path into its segments, each "[]" its own:
// "rows[].env" is rows, [], env; "[]" is the elements of a top-level list.
func splitPath(path string) ([]string, error) {
	var segs []string
	for i, part := range strings.Split(path, ".") {
		name, n := part, 0
		for strings.HasSuffix(name, "[]") {
			name = strings.TrimSuffix(name, "[]")
			n++
		}
		if strings.ContainsAny(name, "[]") || (name == "" && (i > 0 || n == 0)) {
			return nil, fmt.Errorf("malformed path segment %q", part)
		}
		if name != "" {
			segs = append(segs, name)
		}
		for range n {
			segs = append(segs, "[]")
		}
	}
	return segs, nil
}

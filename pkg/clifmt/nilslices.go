package clifmt

import (
	"encoding"
	"encoding/json"
	"reflect"
)

// emptyNilSlices returns a copy of v in which every nil slice reachable
// through exported fields, pointers, interfaces, slices, arrays and map values
// is replaced by an empty one of the same type, so the structured encoders
// write `[]` where encoding/json would write `null`. A list-shaped field that
// is sometimes `null` is not jq-pipeable: `.items[]` dies with "Cannot iterate
// over null" on exactly the empty case a script is least likely to test.
//
// Doing it here, once, below every Emit/Render call site, is the point: a
// per-struct "initialise it before emitting" rule has to be remembered at every
// new payload and was not.
//
// What it deliberately leaves alone:
//   - an untyped nil v: there is no slice type to make, and `null` for "no
//     result" is the published contract (render_empty_test pins it);
//   - []byte, which encoding/json writes as a base64 string, not a list;
//   - nil maps: `null` vs `{}` is a separate contract this does not change;
//   - any value whose type (or pointer to it) implements json.Marshaler or
//     encoding.TextMarshaler, which owns its own encoding;
//   - unexported fields, which the encoders never see.
//
// The input is never mutated; shared or cyclic pointers are followed once per
// path, and a cycle is left as-is for encoding/json to report as it always has.
func emptyNilSlices(v any) any {
	if v == nil {
		return nil
	}
	return emptyNilSlicesValue(reflect.ValueOf(v), map[uintptr]bool{}).Interface()
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

func ownsEncoding(t reflect.Type) bool {
	pt := reflect.PointerTo(t)
	return t.Implements(jsonMarshalerType) || t.Implements(textMarshalerType) ||
		pt.Implements(jsonMarshalerType) || pt.Implements(textMarshalerType)
}

// emptyNilSlicesValue is emptyNilSlices over one value. onPath holds the
// pointers and maps on the current path, so a cycle is left as-is.
func emptyNilSlicesValue(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	if !v.IsValid() || ownsEncoding(v.Type()) {
		return v
	}
	switch v.Kind() {
	case reflect.Pointer:
		return emptyNilInPointer(v, onPath)
	case reflect.Interface:
		return emptyNilInInterface(v, onPath)
	case reflect.Slice:
		return emptyNilInSlice(v, onPath)
	case reflect.Array:
		return emptyNilInArray(v, onPath)
	case reflect.Map:
		return emptyNilInMap(v, onPath)
	case reflect.Struct:
		return emptyNilInStruct(v, onPath)
	default:
		return v
	}
}

// emptyNilInPointer copies what a non-nil, off-path pointer points at.
func emptyNilInPointer(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	if v.IsNil() || onPath[v.Pointer()] {
		return v
	}
	onPath[v.Pointer()] = true
	defer delete(onPath, v.Pointer())
	out := reflect.New(v.Type().Elem())
	out.Elem().Set(emptyNilSlicesValue(v.Elem(), onPath))
	return out
}

// emptyNilInInterface rewraps a non-nil interface's dynamic value.
func emptyNilInInterface(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	if v.IsNil() {
		return v
	}
	out := reflect.New(v.Type()).Elem()
	out.Set(emptyNilSlicesValue(v.Elem(), onPath))
	return out
}

// emptyNilInSlice makes a nil slice empty and copies a non-nil one element
// by element; []byte is left alone (encoding/json writes it as base64).
func emptyNilInSlice(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	if v.Type().Elem().Kind() == reflect.Uint8 {
		return v
	}
	if v.IsNil() {
		return reflect.MakeSlice(v.Type(), 0, 0)
	}
	out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
	for i := range v.Len() {
		out.Index(i).Set(emptyNilSlicesValue(v.Index(i), onPath))
	}
	return out
}

// emptyNilInArray copies an array element by element.
func emptyNilInArray(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	for i := range v.Len() {
		out.Index(i).Set(emptyNilSlicesValue(v.Index(i), onPath))
	}
	return out
}

// emptyNilInMap copies a non-nil, off-path map value by value; a nil map
// stays nil.
func emptyNilInMap(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	if v.IsNil() || onPath[v.Pointer()] {
		return v
	}
	onPath[v.Pointer()] = true
	defer delete(onPath, v.Pointer())
	out := reflect.MakeMapWithSize(v.Type(), v.Len())
	iter := v.MapRange()
	for iter.Next() {
		out.SetMapIndex(iter.Key(), emptyNilSlicesValue(iter.Value(), onPath))
	}
	return out
}

// emptyNilInStruct copies a struct, rewriting its settable (exported)
// fields.
func emptyNilInStruct(v reflect.Value, onPath map[uintptr]bool) reflect.Value {
	out := reflect.New(v.Type()).Elem()
	out.Set(v)
	for i := range v.NumField() {
		if f := out.Field(i); f.CanSet() {
			f.Set(emptyNilSlicesValue(v.Field(i), onPath))
		}
	}
	return out
}

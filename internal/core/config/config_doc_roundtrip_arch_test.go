//go:build arch

package config

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These gates close the legs TestArch_ConfigSave_PersistsEveryConfigDocField
// cannot see: it exercises Marshal, which bypasses toDoc/fromDoc entirely, so a
// field missing from either one broke `config show` and every Unmarshal while
// that gate stayed green (errant-john). Both walk the STRUCTURE, never a list:
// a field added to configDoc or Config is covered the moment it compiles.

// runtimeOnlyTag marks a Config field that is deliberately not persisted.
const runtimeOnlyTag = "runtime"

// fillNonZero sets v (settable) and everything reachable from it to a
// non-zero value. It refuses what it cannot fill rather than skipping it: a
// field left zero here would be a field this gate silently does not check.
func fillNonZero(t *testing.T, v reflect.Value, path string, depth int) {
	t.Helper()
	require.Lessf(t, depth, 12, "%s: type nesting too deep to fill; extend fillNonZero", path)
	if fillScalar(v, path) {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		p := reflect.New(v.Type().Elem())
		fillNonZero(t, p.Elem(), path+"*", depth+1)
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fillNonZero(t, s.Index(0), path+"[0]", depth+1)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fillNonZero(t, k, path+"{key}", depth+1)
		e := reflect.New(v.Type().Elem()).Elem()
		fillNonZero(t, e, path+"{val}", depth+1)
		m.SetMapIndex(k, e)
		v.Set(m)
	case reflect.Struct:
		for i := range v.NumField() {
			f := v.Type().Field(i)
			require.Truef(t, f.IsExported(), "%s.%s is unexported; fillNonZero cannot populate it", path, f.Name)
			fillNonZero(t, v.Field(i), path+"."+f.Name, depth+1)
		}
	case reflect.Interface:
		require.Zerof(t, v.Type().NumMethod(), "%s: non-empty interface %s; fillNonZero cannot pick a concrete type", path, v.Type())
		v.Set(reflect.ValueOf("x-" + path))
	default:
		require.Failf(t, "unfillable kind", "%s: %s", path, v.Kind())
	}
}

// fillScalar sets a scalar kind to a non-zero value, reporting whether v was
// one.
func fillScalar(v reflect.Value, path string) bool {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x-" + path)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1.5)
	case reflect.Bool:
		v.SetBool(true)
	default:
		return false
	}
	return true
}

func fullyFilledConfigDoc(t *testing.T) configDoc {
	t.Helper()
	var doc configDoc
	fillNonZero(t, reflect.ValueOf(&doc).Elem(), "configDoc", 0)
	return doc
}

// TestArch_ConfigDoc_FromDocToDocLosesNothing: every configDoc field, down to
// the leaves of its nested sections, survives fromDoc then toDoc. A field
// fromDoc does not store, toDoc does not emit, or a clone helper drops, fails
// here.
func TestArch_ConfigDoc_FromDocToDocLosesNothing(t *testing.T) {
	want := fullyFilledConfigDoc(t)
	var c Config
	c.fromDoc(fullyFilledConfigDoc(t))
	assert.Equal(t, want, c.toDoc(),
		"a configDoc field did not survive fromDoc→toDoc: config show and Unmarshal silently drop it")
}

// TestArch_Config_EveryPersistedFieldReachesConfigDoc: after fromDoc of a
// fully-filled doc, every Config field NOT marked `config:"runtime"` is set —
// so a field added to Config without a configDoc counterpart fails here — and
// every marked field is still zero, so the mark cannot hide a field fromDoc
// actually writes.
func TestArch_Config_EveryPersistedFieldReachesConfigDoc(t *testing.T) {
	var c Config
	c.fromDoc(fullyFilledConfigDoc(t))
	v := reflect.ValueOf(c)
	for i := range v.NumField() {
		f := v.Type().Field(i)
		tag, marked := f.Tag.Lookup("config")
		if marked {
			require.Equalf(t, runtimeOnlyTag, tag, "Config.%s: unknown config tag value", f.Name)
			assert.Truef(t, v.Field(i).IsZero(),
				"Config.%s is marked runtime-only but fromDoc writes it; drop the mark", f.Name)
			continue
		}
		assert.Falsef(t, v.Field(i).IsZero(), "%s", fmt.Sprintf(
			"Config.%s is persisted (no `config:\"runtime\"` mark) but fromDoc never sets it: "+
				"it has no configDoc field, so it is silently dropped on every load and save", f.Name))
	}
}

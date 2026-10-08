package engine

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergedHome declares the session home, a merged XDG config base owning two
// app dirs, and a plain further var beside it.
func mergedHome() HomeSpec {
	h := validHome()
	h.Vars = []HomeVar{
		{Name: "X_HOME", Subdir: "x"},
		{Name: "XDG_CONFIG_HOME", Subdir: ".xdg/config", Merge: &XDGMerge{Owns: []string{"xapp", "xtool"}}},
		{Name: "X_PLAIN", Subdir: "plain"},
	}
	return h
}

func TestHomeSpec_Validate_AcceptsAMergedXDGBase(t *testing.T) {
	require.NoError(t, mergedHome().Validate())
	h := mergedHome()
	h.Vars[1].Merge.Owns = nil
	require.NoError(t, h.Validate(), "a merged base may own nothing: the session then only shares the user's")
}

// Vars[0] IS the session home: merging the user's base there would land the
// user's tree in the home root.
func TestHomeSpec_Validate_RefusesAMergeOnTheHomeVar(t *testing.T) {
	h := mergedHome()
	h.Vars[0] = HomeVar{Name: "XDG_DATA_HOME", Subdir: "x", Merge: &XDGMerge{}}
	assert.ErrorContains(t, h.Validate(), "Vars[0]")
}

// Only a var the XDG Base Directory spec defines can be merged: its user
// value is resolved by that spec, and nothing else has a defined default.
func TestHomeSpec_Validate_RefusesAMergeOnAVarThatIsNoXDGBase(t *testing.T) {
	for _, name := range []string{"X_PLAIN_MERGED", "XDG_RUNTIME_DIR", "XDG_CONFIG_DIRS"} {
		h := mergedHome()
		h.Vars[1].Name = name
		assert.ErrorContains(t, h.Validate(), name, "var %q", name)
	}
}

// An owned name is one top-level entry of the base, named once.
func TestHomeSpec_Validate_AnOwnedNameIsOneSegmentNamedOnce(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "/abs"} {
		h := mergedHome()
		h.Vars[1].Merge.Owns = []string{bad}
		assert.ErrorContains(t, h.Validate(), "Owns", "owned %q", bad)
	}
	h := mergedHome()
	h.Vars[1].Merge.Owns = []string{"xapp", "xapp"}
	assert.ErrorContains(t, h.Validate(), "xapp")
}

// A merged base's top level is the user's entries and the owned dirs; another
// var's directory there would collide with a user entry of the same name.
func TestHomeSpec_Validate_RefusesAVarInsideAMergedBase(t *testing.T) {
	for _, sub := range []string{".xdg/config/inner", ".xdg/config"} {
		h := mergedHome()
		h.Vars[2].Subdir = sub
		assert.ErrorContains(t, h.Validate(), "merged", "subdir %q", sub)
	}
	h := mergedHome()
	h.Vars[2].Subdir = ".xdg"
	require.NoError(t, h.Validate(), "a plain var CONTAINING a merged base is just a session dir")
}

// The XDG Base Directory spec: a base is its var's value when that is an
// absolute path, else the spec's default under the user's home. A relative
// value is invalid by the spec and is ignored.
func TestUserXDGBase_TheVarWhenAbsoluteElseTheSpecDefault(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "u")
	set := filepath.Join(string(filepath.Separator), "elsewhere", "cfg")
	env := func(v map[string]string) func(string) string { return func(k string) string { return v[k] } }

	got, ok := UserXDGBase("XDG_CONFIG_HOME", env(map[string]string{"XDG_CONFIG_HOME": set}), home)
	require.True(t, ok)
	assert.Equal(t, set, got)

	for name, def := range map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME":  filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
	} {
		got, ok := UserXDGBase(name, env(map[string]string{name: "relative/ignored"}), home)
		require.True(t, ok, name)
		assert.Equal(t, def, got, "%s: a relative value falls back to the default", name)
		got, ok = UserXDGBase(name, env(nil), home)
		require.True(t, ok, name)
		assert.Equal(t, def, got, "%s: unset falls back to the default", name)
	}

	_, ok = UserXDGBase("XDG_RUNTIME_DIR", env(nil), home)
	assert.False(t, ok, "no default: not a mergeable base")
	_, ok = UserXDGBase("XDG_CONFIG_HOME", env(nil), "")
	assert.False(t, ok, "unset with no home resolves nowhere")
}

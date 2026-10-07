package engine

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// multiVarHome declares a home var that names the session home and two
// further vars beneath it, one of them nested.
func multiVarHome() HomeSpec {
	h := validHome()
	h.Vars = []HomeVar{
		{Name: "X_HOME", Subdir: "x"},
		{Name: "X_CACHE", Subdir: "cache"},
		{Name: "X_CONFIG", Subdir: ".xdg/config"},
	}
	return h
}

// THE Q8 RULE: the first declared var names the session home itself; every
// further var names its Subdir beneath it. In place (Engine equals Host) the
// path is the host's own, separators included.
func TestBindHome_TheFirstVarIsTheHome_TheRestAreBeneathIt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "x")
	got := BindHome(multiVarHome().Vars, present.Root{Host: home, Engine: home})
	assert.Equal(t, []HomeBinding{
		{Var: "X_HOME", Path: home},
		{Var: "X_CACHE", Path: filepath.Join(home, "cache")},
		{Var: "X_CONFIG", Path: filepath.Join(home, ".xdg", "config")},
	}, got)
}

// Relocated into a container, the Engine side is a Linux path joined with
// '/', whatever the host's separator.
func TestBindHome_AContainerHomeIsJoinedWithSlashes(t *testing.T) {
	got := BindHome(multiVarHome().Vars, present.Root{Host: filepath.Join("C:", "s", "x"), Engine: "/ctxloom/home/x"})
	assert.Equal(t, []HomeBinding{
		{Var: "X_HOME", Path: "/ctxloom/home/x"},
		{Var: "X_CACHE", Path: "/ctxloom/home/x/cache"},
		{Var: "X_CONFIG", Path: "/ctxloom/home/x/.xdg/config"},
	}, got)
}

// A home with no engine side (unreachable) and an engine with no vars both
// bind nothing: no var may name a path the engine cannot open.
func TestBindHome_NothingBindsWithoutAnEngineSideOrAVar(t *testing.T) {
	assert.Nil(t, BindHome(multiVarHome().Vars, present.Root{Host: "/h"}))
	assert.Nil(t, BindHome(nil, present.Root{Host: "/h", Engine: "/h"}))
}

// The first var's Subdir is the session home's own LEAF (launch.SessionHome
// and the container's instance root both append it as one name), so it is
// one clean segment.
func TestHomeSpec_Validate_TheHomeLeafIsOneSegment(t *testing.T) {
	for _, bad := range []string{"a/b", "..", ".", `a\b`, "/abs"} {
		h := multiVarHome()
		h.Vars[0].Subdir = bad
		assert.ErrorContains(t, h.Validate(), "Vars[0].Subdir", "leaf %q", bad)
	}
}

// A further var's Subdir is a clean relative slash path beneath the home:
// nested is allowed; escaping, absolute, a backslash or the home itself is not.
func TestHomeSpec_Validate_AFurtherVarIsACleanPathBeneathTheHome(t *testing.T) {
	require.NoError(t, multiVarHome().Validate(), "a nested subpath is allowed")
	for _, bad := range []string{"..", "../up", "/abs", `a\b`, "a/../b", ".", "a/"} {
		h := multiVarHome()
		h.Vars[2].Subdir = bad
		assert.ErrorContains(t, h.Validate(), "Vars[2].Subdir", "subdir %q", bad)
	}
}

// Two entries for one var would leave which path the engine gets to map
// order.
func TestHomeSpec_Validate_RefusesARepeatedVar(t *testing.T) {
	h := multiVarHome()
	h.Vars[2].Name = "X_CACHE"
	assert.ErrorContains(t, h.Validate(), "X_CACHE")
}

// A var's directory and the history store's link cannot share a path:
// isolation creates the one as a real directory and links the other.
func TestHomeSpec_Validate_RefusesAVarOverlappingTheHistoryStore(t *testing.T) {
	for _, store := range []string{"cache", "cache/history", ".xdg"} {
		h := multiVarHome()
		h.TranscriptStoreRel = store
		assert.ErrorContains(t, h.Validate(), "TranscriptStoreRel", "store %q", store)
	}
	h := multiVarHome()
	h.TranscriptStoreRel = "cachex"
	require.NoError(t, h.Validate(), "a sibling that merely shares a prefix does not overlap")
}

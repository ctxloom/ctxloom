package confload

import (
	"testing"

	kmaps "github.com/knadh/koanf/maps"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeWith_NilFuncMatchesMergeExactly proves MergeWith(nil, ...) and
// Merge(...) are the SAME call — Merge is documented as being implemented in
// terms of MergeWith, and this pins that byte-for-byte rather than just by
// reading the source.
func TestMergeWith_NilFuncMatchesMergeExactly(t *testing.T) {
	home := map[string]any{"a": 1, "nested": map[string]any{"x": "home", "y": "home-only"}}
	project := map[string]any{"nested": map[string]any{"x": "project"}}

	viaMerge, err := Merge(home, project)
	require.NoError(t, err)
	viaMergeWith, err := MergeWith(nil, home, project)
	require.NoError(t, err)

	assert.Equal(t, viaMerge, viaMergeWith)
}

// atomicReplaceAt is a generic (non-ctxloom) koanf.WithMergeFunc-shaped
// function standing in for ctxloom's own agentBindingMergeFunc: it makes ONE
// named top-level key (topKey) replace wholesale instead of deep-merging,
// while delegating every other key to koanf/maps.Merge — proving the
// WithMergeFunc SEAM itself is generic (confload carries no notion of
// "agents"), and that a caller's func can selectively override just one
// path's merge policy without losing the rest.
func atomicReplaceAt(topKey string) MergeFunc {
	return func(src, dest map[string]any) error {
		atomicVal, has := src[topKey]
		rest := make(map[string]any, len(src))
		for k, v := range src {
			if k == topKey {
				continue
			}
			rest[k] = v
		}
		kmaps.Merge(rest, dest)
		if has {
			dest[topKey] = atomicVal
		}
		return nil
	}
}

// TestMergeWith_CustomFuncReplacesOnePathWholesale proves the koanf.
// WithMergeFunc seam actually takes effect: a higher layer naming "atomic"
// REPLACES the lower layer's whole value there (never fusing sibling
// fields), while an untouched sibling key still deep-merges normally.
func TestMergeWith_CustomFuncReplacesOnePathWholesale(t *testing.T) {
	home := map[string]any{
		"atomic": map[string]any{"permissions": "bypass", "coordinator": true, "runtime": "container"},
		"other":  map[string]any{"x": "home"},
	}
	project := map[string]any{
		"atomic": map[string]any{"profiles": []any{"default"}},
		"other":  map[string]any{"y": "project"},
	}

	out, err := MergeWith(atomicReplaceAt("atomic"), home, project)
	require.NoError(t, err)

	atomic, ok := out["atomic"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{"profiles": []any{"default"}}, atomic,
		"the higher layer's atomic value must replace the lower layer's ENTIRELY -- no field from home may survive")

	other, ok := out["other"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "home", other["x"], "a sibling key not special-cased must still deep-merge normally")
	assert.Equal(t, "project", other["y"])
}

// TestMergeWith_CustomFuncOnlyOneLayerNames_KeepsIt proves the atomic path is
// NOT gap-filled away when only ONE layer names it -- this is "whichever
// layer names it wins", not "a later layer always wins even by omission".
func TestMergeWith_CustomFuncOnlyOneLayerNames_KeepsIt(t *testing.T) {
	home := map[string]any{"atomic": map[string]any{"permissions": "bypass"}}
	project := map[string]any{"other": map[string]any{"y": "project"}} // never mentions "atomic"

	out, err := MergeWith(atomicReplaceAt("atomic"), home, project)
	require.NoError(t, err)

	atomic, ok := out["atomic"].(map[string]any)
	require.True(t, ok, "an agent named only by a lower layer must survive untouched")
	assert.Equal(t, "bypass", atomic["permissions"])
}

// TestMergeWith_DoesNotMutateInputs mirrors Merge's own documented contract:
// callers passed to MergeWith (via a custom func) must not have their
// original maps mutated.
func TestMergeWith_DoesNotMutateInputs(t *testing.T) {
	home := map[string]any{"atomic": map[string]any{"permissions": "bypass"}}
	homeCopy := map[string]any{"atomic": map[string]any{"permissions": "bypass"}}
	project := map[string]any{"atomic": map[string]any{"profiles": []any{"default"}}}

	_, err := MergeWith(atomicReplaceAt("atomic"), home, project)
	require.NoError(t, err)

	assert.Equal(t, homeCopy, home, "MergeWith must not mutate its inputs")
}

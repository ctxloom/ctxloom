package yamlx

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// mapping parses src and returns its root mapping node, comments and all.
func mapping(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &doc))
	require.Equal(t, yaml.DocumentNode, doc.Kind)
	require.Equal(t, yaml.MappingNode, doc.Content[0].Kind)
	return doc.Content[0]
}

func keys(m *yaml.Node) []string {
	var out []string
	for i := 0; i+1 < len(m.Content); i += 2 {
		out = append(out, m.Content[i].Value)
	}
	return out
}

func TestMapValue_ReturnsTheValueNodeForAPresentKey(t *testing.T) {
	m := mapping(t, "a: 1\nb: two\n")
	v := MapValue(m, "b")
	require.NotNil(t, v)
	assert.Equal(t, "two", v.Value)
}

func TestMapValue_NilForAnAbsentKey(t *testing.T) {
	m := mapping(t, "a: 1\n")
	assert.Nil(t, MapValue(m, "zzz"))
}

// A caller holding a document whose head was never parsed (spool's
// comment-only message) asks the same question and must get "absent", not
// a nil dereference.
func TestMapValue_NilMappingIsAbsent(t *testing.T) {
	assert.Nil(t, MapValue(nil, "a"))
}

// The in-place contract every caller relies on: a rewritten value keeps its
// position, and the KEY node — with its comments — is the original one, so
// a user's annotation survives the write.
func TestMapSet_ReplacesInPlaceKeepingPositionAndKeyComments(t *testing.T) {
	m := mapping(t, "a: 1\n# why b\nb: two\nc: 3\n")
	keyBefore := m.Content[2]
	require.Equal(t, "b", keyBefore.Value)
	require.NotEmpty(t, keyBefore.HeadComment)

	MapSet(m, "b", ScalarNode("TWO"))

	assert.Equal(t, []string{"a", "b", "c"}, keys(m))
	assert.Same(t, keyBefore, m.Content[2], "the key node is reused, not rebuilt")
	assert.Equal(t, "TWO", MapValue(m, "b").Value)
}

func TestMapSet_AppendsAStringKeyedPairWhenAbsent(t *testing.T) {
	m := mapping(t, "a: 1\n")
	MapSet(m, "z", ScalarNode("last"))

	assert.Equal(t, []string{"a", "z"}, keys(m))
	key := m.Content[len(m.Content)-2]
	assert.Equal(t, yaml.ScalarNode, key.Kind)
	assert.Equal(t, "!!str", key.Tag)
	assert.Equal(t, "last", MapValue(m, "z").Value)
}

func TestMapDelete_RemovesTheKeyAndItsValue(t *testing.T) {
	m := mapping(t, "a: 1\nb: two\nc: 3\n")
	MapDelete(m, "b")
	assert.Equal(t, []string{"a", "c"}, keys(m))
	assert.Nil(t, MapValue(m, "b"))
}

func TestMapDelete_AbsentKeyIsANoOp(t *testing.T) {
	m := mapping(t, "a: 1\n")
	MapDelete(m, "zzz")
	assert.Equal(t, []string{"a"}, keys(m))
}

func TestEnsureMap_ReturnsAnExistingMapping(t *testing.T) {
	m := mapping(t, "sub:\n  k: v\n")
	sub := EnsureMap(m, "sub")
	assert.Same(t, MapValue(m, "sub"), sub)
	assert.Equal(t, "v", MapValue(sub, "k").Value)
}

func TestEnsureMap_CreatesAMappingWhenAbsentOrOfTheWrongKind(t *testing.T) {
	m := mapping(t, "scalar: 1\n")

	created := EnsureMap(m, "fresh")
	assert.Equal(t, yaml.MappingNode, created.Kind)
	assert.Same(t, MapValue(m, "fresh"), created)

	replaced := EnsureMap(m, "scalar")
	assert.Equal(t, yaml.MappingNode, replaced.Kind)
	assert.Same(t, MapValue(m, "scalar"), replaced, "a scalar under the key is replaced, in place")
	assert.Equal(t, []string{"scalar", "fresh"}, keys(m))
}

func TestScalarNode_IsAPlainStringScalar(t *testing.T) {
	n := ScalarNode("x")
	assert.Equal(t, yaml.ScalarNode, n.Kind)
	assert.Equal(t, "!!str", n.Tag)
	assert.Equal(t, "x", n.Value)
}

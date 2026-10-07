package v3

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func apply(t *testing.T, in string) string {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(in), &doc))
	Step{}.Apply(doc.Content[0])
	out, err := yaml.Marshal(&doc)
	require.NoError(t, err)
	return string(out)
}

func TestStep_DropsLockAndFetchTimes(t *testing.T) {
	in := "schema_version: 2\n" +
		"locked_at: 2026-10-01T00:00:00Z\n" +
		"bundles:\n" +
		"    a:\n" +
		"        sha: abc\n" +
		"        fetched_at: 2026-09-01T00:00:00Z\n" +
		"        held: true\n" +
		"    b:\n" +
		"        sha: def\n"
	want := "schema_version: 2\n" +
		"bundles:\n" +
		"    a:\n" +
		"        sha: abc\n" +
		"        held: true\n" +
		"    b:\n" +
		"        sha: def\n"
	assert.Equal(t, want, apply(t, in))
}

// A lock with no bundles, or a bundles value that is not a mapping, is left
// for the strict decode to judge; the step only removes what it owns.
func TestStep_ToleratesAbsentOrMalformedBundles(t *testing.T) {
	assert.Equal(t, "schema_version: 2\n", apply(t, "schema_version: 2\nlocked_at: 2026-10-01T00:00:00Z\n"))
	assert.Equal(t, "schema_version: 2\nbundles: [x]\n", apply(t, "schema_version: 2\nbundles: [x]\n"))
}

func TestStep_MigratesToThree(t *testing.T) {
	assert.Equal(t, 3, Step{}.To())
}

package profiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The profile ctxloom SEEDS must parse with no unknown keys, or every fresh
// `ctxloom init` warns about a file ctxloom itself just wrote. Not
// hypothetical: the seed carried a `version` key Profile has no field for, and
// a fresh init printed the resulting warning fifteen times.
//
// Asserts against the real shipped resource rather than a fixture: a fixture
// would keep passing while the shipped file drifted, which is the failure this
// exists to stop.
func TestSeededProfile_HasNoKeyTheLoaderRejects(t *testing.T) {
	path := filepath.Join("..", "..", "resources", "profiles", "default.yaml")
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the seeded profile must exist; init copies it verbatim")
	require.NotEmpty(t, raw, "an empty seed would vacuously satisfy the key check below")

	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.NotEmpty(t, doc.Content, "seed must parse to a document with content")

	root := doc.Content[0]
	require.Equal(t, yaml.MappingNode, root.Kind, "seed must be a mapping")

	known := profileKeys()
	require.NotEmpty(t, known, "profileKeys() drives the check; an empty map would pass anything")

	var seen int
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		seen++
		require.Truef(t, known[key],
			"seeded profile declares %q, which profiles.Profile has no field for, so "+
				"warnUnknownProfileKeys fires on a file ctxloom itself wrote", key)
	}
	require.NotZero(t, seen, "seed has no top-level keys — the loop above checked nothing")
}

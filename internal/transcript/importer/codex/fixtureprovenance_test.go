package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFixtureProvenanceIsDocumented pins the invariant this suite's corrected
// header now asserts: the tests validate a FROZEN, checked-in
// capture whose provenance is recorded, not a fresh rollout file. The header
// used to claim the opposite — that running the package validated codex
// "against a fresh rollout file" — which would have let a release-monitoring
// job report a green codex parser against a build it had never seen.
//
// Nothing executable could be red for that: the code was already right, only
// the prose was wrong. So this pins what the corrected prose promises
// instead. If the fixture is renamed or deleted, or its MANIFEST provenance
// entry disappears, the "frozen and documented" claim stops being true and
// this stops passing.
func TestFixtureProvenanceIsDocumented(t *testing.T) {
	for _, name := range []string{"rollout-fixture.jsonl", "golden.transcript.acp.jsonl", "MANIFEST.json"} {
		_, err := os.Stat(filepath.Join("testdata", name))
		require.NoError(t, err, "testdata/%s is what this suite actually validates against", name)
	}

	data, err := os.ReadFile(filepath.Join("testdata", "MANIFEST.json"))
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(data, &manifest), "MANIFEST.json must stay machine-readable")

	entry, ok := manifest["rollout-fixture.jsonl"].(map[string]any)
	require.True(t, ok, "MANIFEST.json must carry a provenance entry for the input fixture")
	assert.NotEmpty(t, entry["real_source"], "the capture this suite freezes must name where it came from")

	golden, ok := manifest["golden.transcript.acp.jsonl"].(map[string]any)
	require.True(t, ok, "MANIFEST.json must carry a provenance entry for the expected output")
	assert.NotEmpty(t, golden["provenance"], "the golden is regenerated from the adapter; that has to stay written down")
}

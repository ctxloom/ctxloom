package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// entryNames reads the order out of a sorted result by MEMBERSHIP on a parsed
// slice, never by matching a dump.
func entryNames(servers []MCPServerEntry) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		out = append(out, s.Name)
	}
	return out
}

// TestSortMCPServers_UnknownSortByIsDeterministicAndLoud pins U085-F23: an
// unrecognised sort_by fell through every switch arm and left the slice in
// whatever order collectMCPServers built it — and that order comes from Go MAP
// ITERATION over the unified and per-backend scopes, so the same input could
// come back in a different order on the next call. Silently ignoring the field
// is the wrong half of the answer twice over: the caller learns nothing and the
// output is unstable.
func TestSortMCPServers_UnknownSortByIsDeterministicAndLoud(t *testing.T) {
	warnings := captureWarnings(t)
	servers := []MCPServerEntry{
		{Name: "zulu", Command: "a"},
		{Name: "alpha", Command: "z"},
		{Name: "mike", Command: "m"},
	}

	sortMCPServers(servers, "nonsense", "")

	assert.Equal(t, []string{"alpha", "mike", "zulu"}, entryNames(servers),
		"an unrecognised sort_by must still produce a STABLE order, not map order")
	assert.Contains(t, warnings.String(), "nonsense",
		"and it must say the field was not understood, naming what it received")
}

// TestSortMCPServers_KnownSortByUnchanged is the characterization half: every
// recognised value keeps behaving exactly as before.
func TestSortMCPServers_KnownSortByUnchanged(t *testing.T) {
	base := []MCPServerEntry{{Name: "zulu", Command: "a"}, {Name: "alpha", Command: "z"}}

	byName := append([]MCPServerEntry(nil), base...)
	sortMCPServers(byName, "", "")
	assert.Equal(t, []string{"alpha", "zulu"}, entryNames(byName), "empty defaults to name")

	byCommand := append([]MCPServerEntry(nil), base...)
	sortMCPServers(byCommand, "command", "")
	assert.Equal(t, []string{"zulu", "alpha"}, entryNames(byCommand))

	desc := append([]MCPServerEntry(nil), base...)
	sortMCPServers(desc, "name", "desc")
	assert.Equal(t, []string{"zulu", "alpha"}, entryNames(desc))
}

// TestListProfiles_UnknownSortByIsDeterministicAndLoud pins the same hole the
// row calls out in ListProfiles. It matters there for the same reason: the
// loader's List emits seeded (bundle-shipped) profiles by ranging over a MAP,
// so an unrecognised sort_by leaves genuinely unstable output.
func TestListProfiles_UnknownSortByIsDeterministicAndLoud(t *testing.T) {
	warnings := captureWarnings(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "profiles"), 0o755))
	for _, name := range []string{"zulu", "alpha", "mike"} {
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "profiles", name+".yaml"),
			[]byte("name: "+name+"\nselect_tags: [a]\n"), 0o644))
	}
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	res, err := ListProfiles(context.Background(), cfg, ListProfilesRequest{SortBy: "nonsense"})
	require.NoError(t, err)
	names := make([]string, 0, len(res.Profiles))
	for _, p := range res.Profiles {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"alpha", "mike", "zulu"}, names)
	assert.Contains(t, warnings.String(), "nonsense")
}

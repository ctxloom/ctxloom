// Tests for the map/weave CLI member surface: how --subagents and -p members
// combine, and that the --part / --parts-from injection is preserved. The fan
// behavior itself (per-member engine/context) is covered by
// operations/ensemble_subagents_test.go; here we cover only the CLI-local glue.
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mergeMembers concatenates named subagents then bare profiles into one ordered
// member list — both map and weave fan across the result.
func TestMergeMembers_SubagentsThenProfiles(t *testing.T) {
	// Named subagents come first, then bare profiles; order within each kind is
	// preserved. This is what `--subagents a,b -p prof1,prof2` resolves to.
	got := mergeMembers([]string{"a", "b"}, []string{"prof1", "prof2"})
	assert.Equal(t, []string{"a", "b", "prof1", "prof2"}, got)

	// Either flag alone works (bare-profile-only is the zero-setup sugar path).
	assert.Equal(t, []string{"prof1", "prof2"}, mergeMembers(nil, []string{"prof1", "prof2"}))
	assert.Equal(t, []string{"a", "b"}, mergeMembers([]string{"a", "b"}, nil))
	assert.Empty(t, mergeMembers(nil, nil))
}

// mapMembers wires the map command's two member flags through mergeMembers.
func TestMapMembers_CombinesFlagSlices(t *testing.T) {
	t.Cleanup(func() { mapSubagents, mapProfiles = nil, nil })
	mapSubagents = []string{"go-cr-security"}
	mapProfiles = []string{"cr-perf", "cr-style"}
	assert.Equal(t, []string{"go-cr-security", "cr-perf", "cr-style"}, mapMembers())
}

// collectInjectedParts (weave --part / --parts-from) is unchanged by Phase C;
// this guards that the injection surface still reads both forms into parts.
func TestCollectInjectedParts_PartAndPartsFrom(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "legacy.txt"), []byte("  old report  \n"), 0o644))

	// The --part file lives OUTSIDE the --parts-from dir so the dir scan doesn't
	// also pick it up.
	named := filepath.Join(t.TempDir(), "named.md")
	require.NoError(t, os.WriteFile(named, []byte("named body"), 0o644))

	parts, err := collectInjectedParts(dir, []string{"audit=" + named})
	require.NoError(t, err)

	// One part per --parts-from file (named by base filename, content trimmed)
	// plus the --part NAME=FILE entry.
	require.Len(t, parts, 2)

	byName := map[string]string{}
	for _, p := range parts {
		byName[p.Profile] = p.Output
	}
	assert.Equal(t, "old report", byName["legacy"], "--parts-from file → part named by filename")
	assert.Equal(t, "named body", byName["audit"], "--part NAME=FILE → part named NAME")
}

// A malformed --part spec is a hard error (NAME=FILE required) — preserved.
func TestCollectInjectedParts_BadPartSpec(t *testing.T) {
	_, err := collectInjectedParts("", []string{"no-equals-sign"})
	assert.Error(t, err)
}

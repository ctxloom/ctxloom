package agents

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeAgentFile(t *testing.T, dir, name, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0644))
}

func TestParseAgent(t *testing.T) {
	sub, err := ParseAgent([]byte("engine: fast\nprofiles:\n  - p1\n  - p2\n"))
	require.NoError(t, err)
	assert.Equal(t, "fast", sub.Engine)
	assert.Equal(t, []string{"p1", "p2"}, sub.Profiles)
	// Name/Source are not encoded in the body — callers assign them.
	assert.Empty(t, sub.Name)
	assert.Empty(t, sub.Source)
}

// TestLoader_ListReadsDirectory proves an agent loads from a
// .ctxloom/agents/<name>.yaml file, with Name from the filename and Source
// from the path.
func TestLoader_ListReadsDirectory(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "dev.yaml", "engine: claude-code\nprofiles: [go-developer]\n")
	writeAgentFile(t, dir, "finder.yaml", "profiles: [finder]\n")

	list, err := NewLoader([]string{dir}).List()
	require.NoError(t, err)
	require.Len(t, list, 2)
	// Sorted by name.
	assert.Equal(t, "dev", list[0].Name)
	assert.Equal(t, "claude-code", list[0].Engine)
	assert.Equal(t, []string{"go-developer"}, list[0].Profiles)
	assert.Equal(t, filepath.Join(dir, "dev.yaml"), list[0].Source)

	assert.Equal(t, "finder", list[1].Name)
	assert.Empty(t, list[1].Engine, "engine is optional")
}

// TestLoader_FaultTolerantBadFile proves a malformed agent file is skipped
// (warned, not fatal) and the valid ones still load — ctxloom fault tolerance.
func TestLoader_FaultTolerantBadFile(t *testing.T) {
	dir := t.TempDir()
	writeAgentFile(t, dir, "good.yaml", "engine: fast\nprofiles: [p1]\n")
	writeAgentFile(t, dir, "bad.yaml", "engine: [this, is, not, a, string\n: : :\n")

	list, err := NewLoader([]string{dir}).List()
	require.NoError(t, err, "a bad file must not fail the whole list")
	require.Len(t, list, 1)
	assert.Equal(t, "good", list[0].Name)
}

func TestLoader_MissingDirectory(t *testing.T) {
	list, err := NewLoader([]string{filepath.Join(t.TempDir(), "nope")}).List()
	require.NoError(t, err)
	assert.Empty(t, list)
}

// TestGetAgentDirs returns only directories that exist on disk.
func TestGetAgentDirs(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(filepath.Join(app, "agents"), 0755))

	dirs := GetAgentDirs([]string{app})
	require.Len(t, dirs, 1)
	assert.Equal(t, filepath.Join(app, "agents"), dirs[0])

	// No agents dir → no entry.
	assert.Empty(t, GetAgentDirs([]string{filepath.Join(root, "absent")}))
}

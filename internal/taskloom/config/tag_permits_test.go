package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// rustTagPermitsFixture is Rust taskloom's own .taskloom/config.yaml as it
// opted into tag permits, verbatim: the block a repo shared by both
// tasklooms carries. cmd/taskloom's tests read the same file.
func rustTagPermitsFixture(t *testing.T) []byte {
	t.Helper()
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	body, err := os.ReadFile(filepath.Join(dir, "testdata", "rust-tag-permits.config.yaml"))
	require.NoError(t, err)
	return body
}

// TestTagPermits_RustBlockLoadsAndIsIgnored pins Go taskloom's side of a
// shared config: a project whose .taskloom/config.yaml declares Rust
// taskloom's tag_permits loads, and the block changes nothing Go resolves.
// Go taskloom stays permissive about tags; the permits are Rust taskloom's
// to enforce.
func TestTagPermits_RustBlockLoadsAndIsIgnored(t *testing.T) {
	body := rustTagPermitsFixture(t)
	require.Contains(t, string(body), "tag_permits:", "the fixture must carry the block under test")

	project := taskstest.ProjectDir(t)
	writeRawConfig(t, project, string(body))

	cfg, err := Load(project, nil)
	require.NoError(t, err, "a config carrying tag_permits must load")
	assert.Equal(t, Config{}, cfg, "tag_permits must decode to nothing: Go taskloom does not act on it")

	mode, err := cfg.ResolveMode("")
	require.NoError(t, err)
	assert.Equal(t, "home", string(mode))
	_, err = cfg.ParsedTagSchema()
	require.NoError(t, err)
}

// TestTagPermits_EveryShapeRustReadsLoads covers the shapes Rust taskloom
// reads beyond the fixture's: flat tags, an empty block, and both keys left
// out.
func TestTagPermits_EveryShapeRustReadsLoads(t *testing.T) {
	for name, block := range map[string]string{
		"flat tags":       "tag_permits:\n  tags: [urgent, release]\n",
		"empty block":     "tag_permits: {}\n",
		"both lists":      "tag_permits:\n  namespaces:\n    area: [cli]\n    relates: open\n  tags:\n    - urgent\n",
		"empty key list":  "tag_permits:\n  namespaces:\n    area: []\n",
		"beside a schema": "tag_schema:\n  - 'tagma.arity:\"area:x\"=scalar'\ntag_permits:\n  namespaces:\n    area: open\n",
	} {
		t.Run(name, func(t *testing.T) {
			project := taskstest.ProjectDir(t)
			writeConfig(t, project, block)
			_, err := Load(project, nil)
			require.NoError(t, err)
		})
	}
}

// TestTagPermits_MalformedBlockIsRefused pins that accepting tag_permits is
// not accepting anything under that name: the schema describes the block's
// real shape, so a block Rust taskloom would refuse is refused here too.
func TestTagPermits_MalformedBlockIsRefused(t *testing.T) {
	for name, block := range map[string]string{
		"not a map":                       "tag_permits: [area]\n",
		"unknown key":                     "tag_permits:\n  flat: [urgent]\n",
		"namespace neither open nor list": "tag_permits:\n  namespaces:\n    area: closed\n",
		"namespace a map":                 "tag_permits:\n  namespaces:\n    area: {cli: true}\n",
		"non-string key":                  "tag_permits:\n  namespaces:\n    area: [1]\n",
		"blank key":                       "tag_permits:\n  namespaces:\n    area: [cli, ' ']\n",
		"blank namespace":                 "tag_permits:\n  namespaces:\n    '': open\n",
		"tags not a list":                 "tag_permits:\n  tags: urgent\n",
		"blank flat tag":                  "tag_permits:\n  tags: ['']\n",
	} {
		t.Run(name, func(t *testing.T) {
			project := taskstest.ProjectDir(t)
			writeConfig(t, project, block)
			_, err := Load(project, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "taskloom: config error")
			assert.Contains(t, err.Error(), "tag_permits")
		})
	}
}

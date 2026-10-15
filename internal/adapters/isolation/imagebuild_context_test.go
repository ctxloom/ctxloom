//go:build !windows

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildBaseImage_BuildsFromItsOwnContext: an on-disk base builds from its
// configured context (a devcontainer's build.context) when it has one, and
// from its Dockerfile's own directory when it has none — the directory its
// COPYs resolve against either way.
func TestBuildBaseImage_BuildsFromItsOwnContext(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}

	dfDir, ctxDir := t.TempDir(), t.TempDir()
	df := filepath.Join(dfDir, "Dockerfile")
	require.NoError(t, os.WriteFile(df, []byte("FROM debian:13\n"), 0o644))

	_, err := buildBaseImage(context.Background(), rt, &baseStage{desc: "configured context", path: df, context: ctxDir}, false, nil)
	require.NoError(t, err)
	_, err = buildBaseImage(context.Background(), rt, &baseStage{desc: "no context", path: df}, false, nil)
	require.NoError(t, err)

	built := buildInvocations(t, logFile)
	require.Len(t, built, 2)
	assert.True(t, strings.HasSuffix(built[0], " "+ctxDir), "the configured context is sent: %s", built[0])
	assert.True(t, strings.HasSuffix(built[1], " "+dfDir), "else the Dockerfile's directory: %s", built[1])
}

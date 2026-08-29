package opencode

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Both context guards used to test `== ""` rather than TrimSpace, so an
// assembled context of "\n" or "  " passed the guard, wrote a 1-4 byte
// .opencode/ctxloom-context.md and pointed opencode at it — a context file
// with nothing in it, indistinguishable from a real delivery.
func TestMaterializeContextSurface_WhitespaceOnlyIsNoOp(t *testing.T) {
	for _, ctxText := range []string{"\n", "   ", " \n\t\n "} {
		fs := afero.NewMemMapFs()
		_, err := materializeContextSurface(fs, "/proj", ctxText)
		require.NoError(t, err)

		exists, err := afero.Exists(fs, filepath.Join("/proj", opencodeContextFile))
		require.NoError(t, err)
		assert.False(t, exists, "a whitespace-only context must not be written as a real context file (%q)", ctxText)
	}
}

// The same guard in the persistent settings writer: whitespace-only content
// must take the "no context" path (remove the file and the instructions
// reference), not write a blank instruction file.
func TestWriteContext_WhitespaceOnlyRemovesRatherThanWrites(t *testing.T) {
	fs := afero.NewMemMapFs()
	w := &OpencodeWriter{FS: fs}

	_, err := w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: "real context"})
	require.NoError(t, err)
	ctxPath := w.contextFilePath("/proj")
	exists, err := afero.Exists(fs, ctxPath)
	require.NoError(t, err)
	require.True(t, exists)

	_, err = w.WriteContext(agent.ContextWriteRequest{ProjectDir: "/proj", Context: "  \n "})
	require.NoError(t, err)
	exists, err = afero.Exists(fs, ctxPath)
	require.NoError(t, err)
	assert.False(t, exists, "a whitespace-only context must clear the surface, not write a blank instruction file")
}

// Setup must run before Chat/launchInteractive or the run silently delivers
// no context, no commands and no skills — connascence of EXECUTION ORDER with
// no assertion anywhere. There is now an assertion: the seam says so out loud
// instead of running a hollow session that looks fine.
func TestAssertSetupRan_SaysSoWhenSetupWasSkipped(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	b := NewOpencode()
	b.assertSetupRan("chat")
	assert.Contains(t, buf.String(), "Setup", "a Chat with no Setup behind it must not run silently")

	buf.Reset()
	b.setupRan = true
	b.assertSetupRan("chat")
	assert.Empty(t, buf.String(), "a normal run (Setup ran) must stay quiet")
}

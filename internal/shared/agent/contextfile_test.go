package agent

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWriteFramedContextFile verifies the framed sibling written for claude's
// --append-system-prompt-file delivery: it wraps the raw context in the shared
// ctxloom framing and lands at <hash>.sysprompt.md next to the raw file.
func TestWriteFramedContextFile(t *testing.T) {
	fs := afero.NewMemMapFs()

	hash, err := WriteContextFile("/work", []*Fragment{{Content: "go rules here"}}, WithContextFS(fs))
	require.NoError(t, err)
	require.NotEmpty(t, hash)

	path, err := WriteFramedContextFile("/work", hash, WithContextFS(fs))
	require.NoError(t, err)
	require.NotEmpty(t, path)
	assert.True(t, strings.HasSuffix(path, hash+SCMFramedContextSuffix),
		"framed file is the .sysprompt.md sibling of the raw context file")

	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	assert.Equal(t, FrameProjectContext("go rules here"), string(data),
		"framed file content is exactly the shared framing over the raw context")
}

// TestWriteFramedContextFile_NoRawContext proves a missing/empty raw context
// yields no framed file (and no error) — nothing to deliver, deliver nothing.
func TestWriteFramedContextFile_NoRawContext(t *testing.T) {
	fs := afero.NewMemMapFs()

	path, err := WriteFramedContextFile("/work", "deadbeefdeadbeef", WithContextFS(fs))
	require.NoError(t, err)
	assert.Empty(t, path, "no raw context ⇒ no framed file")
}

package backends

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewMock(t *testing.T) {
	mock := NewMock()

	assert.Equal(t, "mock", mock.Name())
	assert.Equal(t, "1.0.0", mock.Version())
	assert.NotNil(t, mock.Args)
	assert.NotNil(t, mock.Env)
}

func TestMock_Setup(t *testing.T) {
	mock := NewMock()

	fragments := []*agent.Fragment{
		{Content: "test fragment"},
	}

	req := &agent.SetupRequest{
		WorkDir:   "/test/dir",
		Fragments: fragments,
	}

	err := mock.Setup(context.Background(), req)
	require.NoError(t, err)

	assert.Equal(t, "/test/dir", mock.WorkDir())
	assert.Len(t, mock.fragments, 1)
}

// TestRecordMockInput_CapturesCwdAndConfigHome is the seam this fixes: before,
// recordMockInput recorded only mode/fragment-count/context/prompt, so no
// hermetic test could prove WHERE the engine ran or WHAT isolation env it
// received — the very things isolation tests need to assert on. cwd comes
// from os.Getwd() (the process's REAL cwd, set by launcher.go's cmd.Dir at
// spawn time), not an echoed-back request field, so this genuinely proves
// the boundary rather than a request round-trip.
func TestRecordMockInput_CapturesCwdAndConfigHome(t *testing.T) {
	dir := t.TempDir()
	recordFile := filepath.Join(dir, "record.txt")

	req := &agent.ExecuteRequest{
		Mode: agent.ModeOneshot,
		Env: map[string]string{
			"CLAUDE_CONFIG_DIR": "/agents/one/.claude",
			// CODEX_HOME / KIRO_HOME deliberately absent: only set keys should appear.
		},
	}

	var stderr bytes.Buffer
	recordMockInput(recordFile, req, "some context", "some prompt", 2, &stderr)
	require.Empty(t, stderr.String())

	data, err := os.ReadFile(recordFile)
	require.NoError(t, err)
	content := string(data)

	wantCwd, err := os.Getwd()
	require.NoError(t, err)
	assert.Contains(t, content, "cwd="+wantCwd)
	assert.Contains(t, content, "CLAUDE_CONFIG_DIR=/agents/one/.claude")
	assert.NotContains(t, content, "CODEX_HOME=")
	assert.NotContains(t, content, "KIRO_HOME=")
}

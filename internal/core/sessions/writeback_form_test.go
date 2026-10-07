package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestSidecarSaveIsWriteBackForm: a session sidecar is saved in the encoding
// an upgrade write-back would give it.
func TestSidecarSaveIsWriteBackForm(t *testing.T) {
	m, root := openSidecarRoot(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, m.AppendRotations(e.HarpName, []Rotation{{
		SessionID: "s-1", RotatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}))

	saved, err := os.ReadFile(filepath.Join(root, e.HarpName, paths.SessionSidecarFileName))
	require.NoError(t, err)
	yamlform.RequireWriteBackForm(t, saved)
}

//go:build !windows

package isolation

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/owneronly"
)

// A file the engine reports writing that others can read fails the
// preparation, naming it. Unix only: on Windows a file created in the home
// inherits its owner-only ACE whatever mode it was written with, so there is
// no honest fixture for an engine writing it loose.
func TestPrepareInstanceHome_RefusesAGeneratedFileLooserThanOwnerOnly(t *testing.T) {
	withFakeHome(t)
	instance := t.TempDir()
	loose := filepath.Join(instance, ".claude.json")
	require.NoError(t, os.WriteFile(loose, []byte("{}"), 0o644))
	require.NoError(t, os.Chmod(loose, 0o644))
	withInstanceConfigWriter(t, "claude-code", &recordingInstanceConfig{report: engine.InstanceConfigReport{Wrote: []string{loose}}})

	_, err := PrepareInstanceHome(InstanceHomeRequest{Engine: "claude-code", InstanceHome: instance, WorkDir: t.TempDir()})
	var exposed *owneronly.ExposedError
	require.True(t, errors.As(err, &exposed), "got %v", err)
	assert.Equal(t, loose, exposed.Path)
}

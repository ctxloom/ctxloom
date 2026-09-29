package agent

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSafeCommandRelPath_WindowsRootedAndDriveRelative: the Windows spellings
// that are not filepath.IsAbs yet do not name a path under dir.
func TestSafeCommandRelPath_WindowsRootedAndDriveRelative(t *testing.T) {
	dir := filepath.Join(`C:\work`, ".agents", "skills")
	for _, in := range []string{`\abs\path.md`, `/abs/path.md`, `C:x.md`, `C:\abs\path.md`, `\\server\share\x.md`} {
		got, ok := SafeCommandRelPath(dir, in)
		assert.False(t, ok, "%q must be refused", in)
		assert.Empty(t, got, "%q must join nothing", in)
	}
}

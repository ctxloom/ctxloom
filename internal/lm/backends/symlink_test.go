// Symlink tests verify that SCM correctly sets up the project-local binary
// symlink at .scm/bin/scm. This symlink enables hooks and MCP servers to
// call SCM without requiring it to be in PATH - critical for portable setups
// and containerized environments.
package backends

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Command Generation Tests
// =============================================================================
// These pure functions generate command strings for hooks and MCP servers.
// Using relative paths (.scm/bin/scm) ensures portability across machines.

func TestGetContextInjectionCommand(t *testing.T) {
	tests := []struct {
		name     string
		hash     string
		expected string
	}{
		{
			name:     "standard hash",
			hash:     "abc123def456",
			expected: "./.scm/bin/scm hook inject-context abc123def456",
		},
		{
			name:     "short hash",
			hash:     "abc",
			expected: "./.scm/bin/scm hook inject-context abc",
		},
		{
			name:     "empty hash",
			hash:     "",
			expected: "./.scm/bin/scm hook inject-context ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetContextInjectionCommand(tt.hash)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetSCMMCPCommand(t *testing.T) {
	// Command should be relative path for portability
	cmd := GetSCMMCPCommand()
	assert.Equal(t, "./.scm/bin/scm", cmd)
	assert.Contains(t, cmd, SCMBinDir)
	assert.Contains(t, cmd, SCMBinaryName)
}

func TestGetSCMMCPArgs(t *testing.T) {
	args := GetSCMMCPArgs()
	assert.Equal(t, []string{"mcp"}, args)
}

// =============================================================================
// Symlink Option Tests
// =============================================================================
// Functional options enable testing without touching the real filesystem.

func TestWithSymlinkFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	opt := WithSymlinkFS(fs)

	options := &symlinkOptions{}
	opt(options)

	assert.Equal(t, fs, options.fs)
}

func TestWithExecPath(t *testing.T) {
	opt := WithExecPath("/custom/path/to/scm")

	options := &symlinkOptions{}
	opt(options)

	assert.Equal(t, "/custom/path/to/scm", options.execPath)
}

func TestApplySymlinkOptions_Defaults(t *testing.T) {
	// Default options should use OS filesystem
	options := applySymlinkOptions(nil)

	assert.NotNil(t, options.fs)
	assert.Empty(t, options.execPath)
}

func TestApplySymlinkOptions_WithCustomFS(t *testing.T) {
	fs := afero.NewMemMapFs()
	options := applySymlinkOptions([]SymlinkOption{
		WithSymlinkFS(fs),
		WithExecPath("/test/scm"),
	})

	assert.Equal(t, fs, options.fs)
	assert.Equal(t, "/test/scm", options.execPath)
}

// =============================================================================
// Symlink Creation Tests
// =============================================================================
// EnsureSCMSymlink creates .scm/bin/scm pointing to the running binary.
// Uses real filesystem since MemMapFs doesn't support symlinks.

func TestEnsureSCMSymlink_CreatesDirectory(t *testing.T) {
	// Uses real tmpdir because MemMapFs doesn't support symlinks
	tmpDir := t.TempDir()

	symlinkPath, err := EnsureSCMSymlink(tmpDir, WithExecPath("/usr/bin/scm"))
	require.NoError(t, err)

	// Verify bin directory was created
	binDir := filepath.Join(tmpDir, SCMBinDir)
	info, err := os.Stat(binDir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Verify symlink path is correct
	assert.Equal(t, filepath.Join(tmpDir, SCMBinDir, SCMBinaryName), symlinkPath)
}

func TestEnsureSCMSymlink_CreatesSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a real file to symlink to
	srcPath := filepath.Join(tmpDir, "scm-binary")
	require.NoError(t, os.WriteFile(srcPath, []byte("binary"), 0755))

	symlinkPath, err := EnsureSCMSymlink(tmpDir, WithExecPath(srcPath))
	require.NoError(t, err)

	// Verify symlink was created
	info, err := os.Lstat(symlinkPath)
	require.NoError(t, err)
	assert.True(t, info.Mode()&os.ModeSymlink != 0, "should be a symlink")

	// Verify symlink target
	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, srcPath, target)
}

func TestEnsureSCMSymlink_UpdatesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create initial symlink pointing to old path
	binDir := filepath.Join(tmpDir, SCMBinDir)
	require.NoError(t, os.MkdirAll(binDir, 0755))

	oldTarget := filepath.Join(tmpDir, "old-binary")
	require.NoError(t, os.WriteFile(oldTarget, []byte("old"), 0755))
	oldSymlink := filepath.Join(binDir, SCMBinaryName)
	require.NoError(t, os.Symlink(oldTarget, oldSymlink))

	// Create new binary and update symlink
	newTarget := filepath.Join(tmpDir, "new-binary")
	require.NoError(t, os.WriteFile(newTarget, []byte("new"), 0755))

	symlinkPath, err := EnsureSCMSymlink(tmpDir, WithExecPath(newTarget))
	require.NoError(t, err)

	// Verify symlink points to new target
	target, err := os.Readlink(symlinkPath)
	require.NoError(t, err)
	assert.Equal(t, newTarget, target)
}

func TestEnsureSCMSymlink_NestedWorkDir(t *testing.T) {
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "deep", "nested", "project")
	require.NoError(t, os.MkdirAll(nestedDir, 0755))

	srcPath := filepath.Join(tmpDir, "scm-binary")
	require.NoError(t, os.WriteFile(srcPath, []byte("binary"), 0755))

	symlinkPath, err := EnsureSCMSymlink(nestedDir, WithExecPath(srcPath))
	require.NoError(t, err)

	// Verify symlink exists in nested directory
	expectedPath := filepath.Join(nestedDir, SCMBinDir, SCMBinaryName)
	assert.Equal(t, expectedPath, symlinkPath)

	_, err = os.Lstat(symlinkPath)
	require.NoError(t, err)
}

// =============================================================================
// Constants Tests
// =============================================================================
// Verify constants match expected values for stable API.

func TestConstants(t *testing.T) {
	assert.Equal(t, ".scm/bin", SCMBinDir)
	assert.Equal(t, "scm", SCMBinaryName)
}

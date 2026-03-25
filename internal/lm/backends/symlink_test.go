// Symlink tests verify the executable path resolution and command generation.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetExecutablePath(t *testing.T) {
	// Set a test path
	SetExecutablePathForTesting("/test/path/to/scm")
	defer SetExecutablePathForTesting("") // Reset after test

	path, err := GetExecutablePath()
	assert.NoError(t, err)
	assert.Equal(t, "/test/path/to/scm", path)
}

func TestGetContextInjectionCommand(t *testing.T) {
	SetExecutablePathForTesting("/usr/local/bin/scm")
	defer SetExecutablePathForTesting("")

	tests := []struct {
		name     string
		hash     string
		workDir  string
		expected string
	}{
		{
			name:     "standard hash with absolute workDir",
			hash:     "abc123def456",
			workDir:  "/home/user/project",
			expected: "/usr/local/bin/scm hook inject-context --project /home/user/project abc123def456",
		},
		{
			name:     "short hash",
			hash:     "abc",
			workDir:  "/project",
			expected: "/usr/local/bin/scm hook inject-context --project /project abc",
		},
		{
			name:     "empty hash",
			hash:     "",
			workDir:  "/project",
			expected: "/usr/local/bin/scm hook inject-context --project /project ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetContextInjectionCommand(tt.hash, tt.workDir)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetSCMMCPCommand(t *testing.T) {
	SetExecutablePathForTesting("/home/user/go/bin/scm")
	defer SetExecutablePathForTesting("")

	cmd := GetSCMMCPCommand()
	assert.Equal(t, "/home/user/go/bin/scm", cmd)
}

func TestGetSCMMCPArgs(t *testing.T) {
	args := GetSCMMCPArgs()
	assert.Equal(t, []string{"mcp"}, args)
}

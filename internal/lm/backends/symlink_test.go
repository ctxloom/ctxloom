// Symlink tests verify the executable path resolution and command generation.
package backends

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetExecutablePath(t *testing.T) {
	// Set a test path
	SetExecutablePathForTesting("/test/path/to/ctxloom")
	defer SetExecutablePathForTesting("") // Reset after test

	path, err := GetExecutablePath()
	assert.NoError(t, err)
	assert.Equal(t, "/test/path/to/ctxloom", path)
}

func TestGetContextInjectionCommand(t *testing.T) {
	SetExecutablePathForTesting("/usr/local/bin/ctxloom")
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
			expected: `"/usr/local/bin/ctxloom" hook inject-context --project "/home/user/project" abc123def456`,
		},
		{
			name:     "short hash",
			hash:     "abc",
			workDir:  "/project",
			expected: `"/usr/local/bin/ctxloom" hook inject-context --project "/project" abc`,
		},
		{
			name:     "empty hash",
			hash:     "",
			workDir:  "/project",
			expected: `"/usr/local/bin/ctxloom" hook inject-context --project "/project" `,
		},
		{
			name:     "path with spaces",
			hash:     "abc123",
			workDir:  "/home/user/my project",
			expected: `"/usr/local/bin/ctxloom" hook inject-context --project "/home/user/my project" abc123`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetContextInjectionCommand(tt.hash, tt.workDir)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetCtxloomMCPCommand(t *testing.T) {
	SetExecutablePathForTesting("/home/user/go/bin/ctxloom")
	defer SetExecutablePathForTesting("")

	cmd := GetCtxloomMCPCommand()
	assert.Equal(t, "/home/user/go/bin/ctxloom", cmd)
}

func TestGetCtxloomMCPArgs(t *testing.T) {
	args := GetCtxloomMCPArgs()
	assert.Equal(t, []string{"mcp"}, args)
}

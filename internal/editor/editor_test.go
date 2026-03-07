package editor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Constructor Tests
//
// These tests verify that the Editor is correctly instantiated with the
// provided configuration values.
// =============================================================================

// TestNew_BasicConstruction verifies that New() creates an Editor with the
// correct command and arguments stored. This is essential for the Editor to
// later launch the correct external program.
func TestNew_BasicConstruction(t *testing.T) {
	ed := New("vim", []string{"-n", "--noplugin"})

	assert.NotNil(t, ed)
	assert.Equal(t, "vim", ed.command)
	assert.Equal(t, []string{"-n", "--noplugin"}, ed.args)
}

// TestNew_EmptyArgs verifies that an Editor can be created with no arguments.
// Many editors work fine with just the command and filepath.
func TestNew_EmptyArgs(t *testing.T) {
	ed := New("nano", nil)

	assert.NotNil(t, ed)
	assert.Equal(t, "nano", ed.command)
	assert.Nil(t, ed.args)
}

// TestNew_ComplexCommand verifies that editors with path separators are
// handled correctly. Users may specify full paths to their editor.
func TestNew_ComplexCommand(t *testing.T) {
	ed := New("/usr/local/bin/code", []string{"--wait", "--new-window"})

	assert.Equal(t, "/usr/local/bin/code", ed.command)
	assert.Equal(t, []string{"--wait", "--new-window"}, ed.args)
}

// =============================================================================
// Command() Tests
//
// Tests for retrieving the configured editor command.
// =============================================================================

// TestCommand_ReturnsConfiguredEditor verifies that Command() returns the
// same editor command that was passed to New(). This allows callers to
// display which editor will be used.
func TestCommand_ReturnsConfiguredEditor(t *testing.T) {
	tests := []struct {
		name    string
		command string
	}{
		{"simple command", "vim"},
		{"full path", "/usr/bin/vim"},
		{"with spaces in path", "/Applications/Visual Studio Code.app/Contents/MacOS/code"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ed := New(tt.command, nil)
			assert.Equal(t, tt.command, ed.Command())
		})
	}
}

// =============================================================================
// Edit() Tests
//
// These tests verify the file editing behavior. Since Edit() launches external
// processes, we use a mock command that exits quickly for testing.
// =============================================================================

// TestEdit_NonexistentCommand verifies that Edit() returns an appropriate
// error when the configured editor command doesn't exist. This provides
// clear feedback to users with misconfigured editors.
func TestEdit_NonexistentCommand(t *testing.T) {
	ed := New("/nonexistent/editor/command", nil)
	tmpFile := filepath.Join(t.TempDir(), "test.txt")

	err := ed.Edit(tmpFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "editor failed")
}

// TestEdit_CommandExitsWithError verifies that editor exit codes are
// propagated as errors. This catches cases where the editor encounters
// problems (e.g., read-only file, disk full).
func TestEdit_CommandExitsWithError(t *testing.T) {
	// Use 'false' command which always exits with code 1
	ed := New("false", nil)
	tmpFile := filepath.Join(t.TempDir(), "test.txt")

	err := ed.Edit(tmpFile)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "editor failed")
}

// TestEdit_SuccessfulEdit verifies that Edit() succeeds when the editor
// command runs and exits successfully.
func TestEdit_SuccessfulEdit(t *testing.T) {
	// Use 'true' command which always exits with code 0
	ed := New("true", nil)
	tmpFile := filepath.Join(t.TempDir(), "test.txt")

	err := ed.Edit(tmpFile)
	assert.NoError(t, err)
}

// TestEdit_FilePathPassedToEditor verifies that the filepath is appended
// to the editor arguments. We can't easily verify this without a mock,
// but we can at least ensure no panic occurs with various path formats.
func TestEdit_FilePathPassedToEditor(t *testing.T) {
	ed := New("true", []string{"--wait"})

	paths := []string{
		"/tmp/test.txt",
		"./relative/path.md",
		"file with spaces.txt",
		"/path/with/many/components/file.yaml",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			err := ed.Edit(path)
			assert.NoError(t, err)
		})
	}
}

// =============================================================================
// EditWithTemplate() Tests
//
// These tests verify template pre-population behavior when editing files.
// =============================================================================

// TestEditWithTemplate_CreatesFileWithTemplate verifies that when the target
// file doesn't exist, it's created with the provided template content before
// opening the editor. This helps users get started with proper structure.
func TestEditWithTemplate_CreatesFileWithTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "new-file.md")
	template := "# New Document\n\nStart writing here...\n"

	ed := New("true", nil) // 'true' exits immediately
	err := ed.EditWithTemplate(targetFile, template)
	require.NoError(t, err)

	// Verify file was created with template content
	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, template, string(content))
}

// TestEditWithTemplate_PreservesExistingFile verifies that existing files
// are opened as-is without overwriting their content with the template.
// This prevents accidental data loss.
func TestEditWithTemplate_PreservesExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "existing.md")
	existingContent := "# Existing Content\n\nImportant data here!\n"
	template := "# Template\n\nThis should NOT overwrite existing content.\n"

	// Create the file first
	require.NoError(t, os.WriteFile(targetFile, []byte(existingContent), 0644))

	ed := New("true", nil)
	err := ed.EditWithTemplate(targetFile, template)
	require.NoError(t, err)

	// Verify original content is preserved
	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, existingContent, string(content))
}

// TestEditWithTemplate_CreatesParentDirectories verifies behavior when the
// parent directory doesn't exist. Note: the current implementation does NOT
// create parent directories - this documents the expected behavior.
func TestEditWithTemplate_FailsWithoutParentDir(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "nonexistent", "subdir", "file.md")
	template := "content"

	ed := New("true", nil)
	err := ed.EditWithTemplate(targetFile, template)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create file")
}

// TestEditWithTemplate_EditorFailure verifies that editor failures are
// reported even when template creation succeeds. The file should still
// exist with template content.
func TestEditWithTemplate_EditorFailure(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "test.md")
	template := "template content"

	ed := New("false", nil) // 'false' always exits with error
	err := ed.EditWithTemplate(targetFile, template)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "editor failed")

	// File should still have been created with template
	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Equal(t, template, string(content))
}

// TestEditWithTemplate_EmptyTemplate verifies that an empty template
// creates an empty file. This is valid for starting fresh.
func TestEditWithTemplate_EmptyTemplate(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "empty.md")

	ed := New("true", nil)
	err := ed.EditWithTemplate(targetFile, "")
	require.NoError(t, err)

	content, err := os.ReadFile(targetFile)
	require.NoError(t, err)
	assert.Empty(t, content)
}

// TestEditWithTemplate_FilePermissions verifies that created files have
// appropriate permissions (0644 = rw-r--r--).
func TestEditWithTemplate_FilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	targetFile := filepath.Join(tmpDir, "permtest.md")

	ed := New("true", nil)
	err := ed.EditWithTemplate(targetFile, "content")
	require.NoError(t, err)

	info, err := os.Stat(targetFile)
	require.NoError(t, err)
	// Check that file is readable/writable by owner, readable by others
	perm := info.Mode().Perm()
	assert.Equal(t, os.FileMode(0644), perm)
}

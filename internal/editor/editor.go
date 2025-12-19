// Package editor provides utilities for launching external editors.
package editor

import (
	"fmt"
	"os"
	"os/exec"
)

// Editor handles launching external text editors.
type Editor struct {
	command string
	args    []string
}

// New creates a new Editor with the given command and arguments.
func New(command string, args []string) *Editor {
	return &Editor{
		command: command,
		args:    args,
	}
}

// Edit opens the specified file in the editor and waits for it to close.
// Returns an error if the editor fails to launch or exits with an error.
func (e *Editor) Edit(filepath string) error {
	args := append(e.args, filepath)
	cmd := exec.Command(e.command, args...)

	// Connect to the terminal for interactive editing
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor failed: %w", err)
	}

	return nil
}

// EditWithTemplate opens a file in the editor, pre-populating it with template content
// if the file doesn't exist. Returns an error if the editor fails.
func (e *Editor) EditWithTemplate(filepath, template string) error {
	// Check if file exists
	if _, err := os.Stat(filepath); os.IsNotExist(err) {
		// Create file with template content
		if err := os.WriteFile(filepath, []byte(template), 0644); err != nil {
			return fmt.Errorf("failed to create file with template: %w", err)
		}
	}

	return e.Edit(filepath)
}

// Command returns the editor command.
func (e *Editor) Command() string {
	return e.command
}

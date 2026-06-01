// Tests for stdinConfirmer, the y/N confirm reader behind the interactive
// `bundle delete` prompt. The bundle create/edit/delete commands themselves are
// thin wrappers over internal/operations (covered by operations/bundles_test.go
// and items_test.go); the confirm reader is the remaining CLI-local logic worth
// pinning.
package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStdinConfirmer_AcceptsY(t *testing.T) {
	confirm := stdinConfirmer(strings.NewReader("y\n"))
	assert.True(t, confirm("prompt? "))
}

func TestStdinConfirmer_AcceptsCapitalY(t *testing.T) {
	confirm := stdinConfirmer(strings.NewReader("Y\n"))
	assert.True(t, confirm("prompt? "))
}

func TestStdinConfirmer_RejectsAnythingElse(t *testing.T) {
	for _, input := range []string{"n", "no", "yes", "yep", ""} {
		t.Run(input, func(t *testing.T) {
			confirm := stdinConfirmer(strings.NewReader(input + "\n"))
			assert.False(t, confirm("prompt? "),
				"only literal y/Y counts as confirmation; %q must be no", input)
		})
	}
}

func TestStdinConfirmer_EOFIsNo(t *testing.T) {
	// Empty reader → Fscanln err with answer == "" → reads as no, not a panic.
	confirm := stdinConfirmer(strings.NewReader(""))
	assert.False(t, confirm("prompt? "), "EOF without input must read as no, not a panic")
}

func TestStdinConfirmer_ReadErrorIsNo(t *testing.T) {
	// An io.Reader that errors on read also returns no (errReader is defined in
	// session_cmd_test.go).
	confirm := stdinConfirmer(errReader{})
	assert.False(t, confirm("prompt? "))
}

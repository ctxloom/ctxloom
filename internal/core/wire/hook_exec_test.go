package wire

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHook_Line: a shell-form hook's line is its Command, verbatim; an
// exec-form hook's line quotes the executable and every argument, so a shell
// handed the line runs exactly the argv — spaces, $, backticks and a quote
// inside one argument included.
func TestHook_Line(t *testing.T) {
	assert.Equal(t, `ctxloom hook next-step`, Hook{Command: `ctxloom hook next-step`}.Line())
	assert.Equal(t, `'ctxloom' 'hook' 'next-step'`, Hook{Command: "ctxloom", Args: []string{"hook", "next-step"}}.Line())
	assert.Equal(t, `'/a b/ctx' 'it'\''s $HOME' '`+"`x`"+`'`,
		Hook{Command: "/a b/ctx", Args: []string{"it's $HOME", "`x`"}}.Line())
}

// TestHook_ExecFormIsItsOwnIdentity: two exec hooks naming one executable
// are different hooks when their arguments differ, and one equals the shell
// hook whose line runs the same argv.
func TestHook_ExecFormIsItsOwnIdentity(t *testing.T) {
	a := Hook{Type: "command", Command: "ctxloom", Args: []string{"hook", "next-step"}}
	b := Hook{Type: "command", Command: "ctxloom", Args: []string{"hook", "mail-drain"}}
	assert.Len(t, appendUniqueHooks([]Hook{a}, []Hook{b}), 2, "the arguments are part of what runs")
	assert.Len(t, appendUniqueHooks([]Hook{a}, []Hook{a}), 1)
}

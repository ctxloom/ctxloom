package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestParseProfileSelection(t *testing.T) {
	list := []operations.ProfileEntry{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}

	cases := []struct {
		in       string
		wantName string
		wantQuit bool
		wantOK   bool
	}{
		{"1", "alpha", false, true},
		{"3", "gamma", false, true},
		{"", "", false, true},      // bare Enter → none
		{"n", "", false, true},     // none
		{"none", "", false, true},  // none (word)
		{"q", "", true, true},      // quit
		{"QUIT", "", true, true},   // case-insensitive
		{"0", "", false, false},    // out of range
		{"4", "", false, false},    // out of range
		{"frog", "", false, false}, // not a number/keyword
	}
	for _, c := range cases {
		choice, ok := parseProfileSelection(c.in, list)
		assert.Equal(t, c.wantOK, ok, "ok for %q", c.in)
		assert.Equal(t, c.wantName, choice.Name, "name for %q", c.in)
		assert.Equal(t, c.wantQuit, choice.Quit, "quit for %q", c.in)
	}
}

func TestRunProfilePicker_SelectsByRow(t *testing.T) {
	list := []operations.ProfileEntry{{Name: "alpha"}, {Name: "beta", Description: "second"}}
	var out strings.Builder

	choice := runProfilePicker(list, strings.NewReader("2\n"), &out)

	assert.Equal(t, "beta", choice.Name)
	assert.False(t, choice.Quit)
	// Menu rendered both rows, with the description for the second.
	assert.Contains(t, out.String(), "[1] alpha")
	assert.Contains(t, out.String(), "[2] beta — second")
}

func TestRunProfilePicker_RepromptsThenSelects(t *testing.T) {
	list := []operations.ProfileEntry{{Name: "alpha"}}
	var out strings.Builder

	choice := runProfilePicker(list, strings.NewReader("99\n1\n"), &out)

	assert.Equal(t, "alpha", choice.Name)
	assert.Contains(t, out.String(), "invalid selection")
}

func TestRunProfilePicker_Quit(t *testing.T) {
	list := []operations.ProfileEntry{{Name: "alpha"}}
	choice := runProfilePicker(list, strings.NewReader("q\n"), &strings.Builder{})
	assert.True(t, choice.Quit)
	assert.Empty(t, choice.Name)
}

func TestRunProfilePicker_EOFDegradesToNone(t *testing.T) {
	list := []operations.ProfileEntry{{Name: "alpha"}}
	choice := runProfilePicker(list, strings.NewReader(""), &strings.Builder{})
	assert.False(t, choice.Quit)
	assert.Empty(t, choice.Name) // proceed without a profile
}

package agentkey

import (
	"io/fs"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePublicKey_UnexpandableTilde_BlamesHomeNotAMissingFile pins the
// invariant that a "~" which cannot be expanded is reported as what it is.
//
// The defect this pins: expandHome discarded os.UserHomeDir's error and
// returned the path untouched, so "~/.ssh/id_ed25519.pub" was handed to
// ReadFile with the tilde still in it. That path does not exist under any
// name, so the user was told the file was missing — sending them to look for
// a key they have, instead of at the environment that has no $HOME. The
// swallowed error is the only place the real cause was ever known.
func TestResolvePublicKey_UnexpandableTilde_BlamesHomeNotAMissingFile(t *testing.T) {
	t.Setenv("HOME", "")

	// The fixture is only hostile if os.UserHomeDir actually fails here;
	// assert that from the code-under-test's point of view before asserting
	// anything about behaviour.
	if _, err := os.UserHomeDir(); err == nil {
		t.Skip("this platform resolves a home directory without $HOME; the fault cannot be injected")
	}

	d := &Discoverer{
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
	}

	_, err := d.resolvePublicKey("~/.ssh/id_ed25519.pub")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "HOME",
		"an unexpandable ~ must name the environment as the cause, not a missing file: %q", err.Error())
}

package agentkey

import (
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolvePublicKey_UnexpandableTilde_BlamesHomeNotAMissingFile pins the
// invariant that a "~" which cannot be expanded is reported as what it is.
//
// A tilde left in place would hand "~/.ssh/id_ed25519.pub" to ReadFile with
// the tilde still in it. That path exists under no name, so the user would
// be told the file is missing — sent to look for a key they have, instead of
// at the composition that handed the discoverer no home.
func TestResolvePublicKey_UnexpandableTilde_BlamesHomeNotAMissingFile(t *testing.T) {
	// A discoverer composed with no home: the fault is the missing home, and
	// the error must say so rather than report the key file as absent.
	d := &Discoverer{
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
	}

	_, err := d.resolvePublicKey("~/.ssh/id_ed25519.pub")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "home directory",
		"an unexpandable ~ must name the missing home as the cause, not a missing file: %q", err.Error())
}

package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestRunStartHandoff_RoundTripFileOnly proves the §5.A4 handoff: the host
// serializes RunStart to a 0600 file under the session persist dir (NEVER argv
// or env — it can carry fragments and env values), and the in-container turn
// process decodes it and DELETES it. The perms and the delete-after-read are
// the security-relevant assertions.
func TestRunStartHandoff_RoundTripFileOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const harp = "regal-rash-dash"

	req := &pb.RunStart{
		Prompt: &pb.Fragment{Content: "the prompt"},
		Fragments: []*pb.Fragment{
			{Content: "a big fragment that would be silly and unsafe on argv"},
		},
		Options: &pb.RunOptions{
			WorkDir: "/work",
			Env:     map[string]string{"CTXLOOM_SESSION_HARP": harp, "SOME_SECRET_ISH": "value-not-on-argv"},
		},
	}

	path, err := writeRunStartHandoff(harp, req)
	require.NoError(t, err)

	// Written under the session persist dir (the bind-mounted, harp-scoped dir).
	persist, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(persist, "runstart.json"), path)

	// 0600: owner-only — the file carries prompt/fragments/env values.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the handoff file must be 0600")

	// The in-container half decodes the SAME payload …
	got, err := readRunStartHandoff(path)
	require.NoError(t, err)
	assert.Equal(t, "the prompt", got.GetPrompt().GetContent())
	require.Len(t, got.GetFragments(), 1)
	assert.Equal(t, "value-not-on-argv", got.GetOptions().GetEnv()["SOME_SECRET_ISH"])

	// … and DELETES the file, so the payload does not linger.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "readRunStartHandoff must delete the file after decoding")
}

// TestReadRunStartHandoff_MissingFileErrors: a missing handoff is a loud error,
// never a silent empty RunStart (which would run the engine context-free — the
// project's signature silent-no-op).
func TestReadRunStartHandoff_MissingFileErrors(t *testing.T) {
	_, err := readRunStartHandoff(filepath.Join(t.TempDir(), "nope.json"))
	require.Error(t, err)
}

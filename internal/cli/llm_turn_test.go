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

// TestReadRunStartHandoff_CorruptFileIsPreserved pins U037-F24: the unlink was
// registered as a `defer` BEFORE the decode, so a handoff whose bytes are
// malformed got deleted on the way out — destroying the only evidence of why
// the turn failed and making the failure unreproducible. The delete is a
// privacy measure for a payload that DID reach the engine; a payload that
// never decoded has nothing to scrub and everything to explain.
func TestReadRunStartHandoff_CorruptFileIsPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runstart.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"options": not-json`), 0o600))

	_, err := readRunStartHandoff(path)
	require.Error(t, err, "a corrupt handoff must be a loud error")

	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "a corrupt handoff must survive the failed read as evidence")
}

// TestReadRunStartHandoff_EmptyPayloadErrors pins U037-F08 on the READ side.
// readRunStartHandoff's contract is "never a silent empty RunStart (which would
// run the engine context-free)", but a well-formed `{}` decoded cleanly into a
// zero-value RunStart and RunTurn launched the engine with no options, no
// prompt, no fragments and no managed config — exit 0, engine up, zero context
// delivered.
func TestReadRunStartHandoff_EmptyPayloadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runstart.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0o600))

	_, err := readRunStartHandoff(path)
	require.Error(t, err, "a handoff carrying nothing must not run the engine context-free")

	_, statErr := os.Stat(path)
	assert.NoError(t, statErr, "a rejected handoff survives as evidence, like a corrupt one")
}

// TestWriteRunStartHandoff_EmptyPayloadErrors pins U037-F08 on the WRITE side:
// the host must not publish a handoff that carries nothing. Catching it here
// fails the launch at the place that can still say what went wrong, instead of
// inside a container exec.
func TestWriteRunStartHandoff_EmptyPayloadErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	_, err := writeRunStartHandoff("regal-rash-dash", &pb.RunStart{})
	require.Error(t, err, "an empty RunStart must not be handed off")

	_, err = writeRunStartHandoff("regal-rash-dash", nil)
	require.Error(t, err, "a nil RunStart must not be handed off")
}

// TestRunStartHandoff_MinimalOptionsOnlyPayloadIsAccepted is the other half of
// the U037-F08 floor, and the reason the guard tests the WHOLE message rather
// than any single field: an interactive turn legitimately carries no prompt and
// no fragments (the user types into the engine's own TTY), and skip_setup runs
// carry no managed config. Only a RunStart with nothing at all set is rejected.
func TestRunStartHandoff_MinimalOptionsOnlyPayloadIsAccepted(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	path, err := writeRunStartHandoff("regal-rash-dash", &pb.RunStart{
		Options: &pb.RunOptions{WorkDir: "/work"},
	})
	require.NoError(t, err, "options-only is a real interactive turn, not an empty handoff")

	got, err := readRunStartHandoff(path)
	require.NoError(t, err)
	assert.Equal(t, "/work", got.GetOptions().GetWorkDir())
}

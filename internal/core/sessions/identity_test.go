package sessions

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Identity is the one trustworthy session identity; the two env codecs are the
// only process-boundary carriers of it. Each codec is pinned by a round trip
// (encode → decode equality) and by one malformed input it refuses, so a key
// cannot be renamed on one side without the other noticing.

func lookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestIdentity_IsChild_IsLeaf_WithRun(t *testing.T) {
	root := Identity{Harp: "quiet-amber-falcon", Depth: 0}
	assert.False(t, root.IsChild())
	assert.False(t, root.IsLeaf(2), "depth 0 under a cap of 2 may still delegate")
	child := root.WithRun("run-7")
	assert.Equal(t, "run-7", child.RunID)
	assert.Equal(t, "", root.RunID, "WithRun returns a copy; the receiver is a value")

	deep := Identity{Harp: "quiet-amber-falcon", Depth: 2}
	assert.True(t, deep.IsChild())
	assert.True(t, deep.IsLeaf(2), "depth >= cap is a leaf")
	one := Identity{Harp: "quiet-amber-falcon", Depth: 0, OneShot: true}
	assert.True(t, one.IsLeaf(99), "a one-shot run is a leaf regardless of depth")
}

func TestIdentity_Validate_IsTheHarpRule(t *testing.T) {
	assert.NoError(t, Identity{Harp: "quiet-amber-falcon"}.Validate())
	assert.Error(t, Identity{}.Validate(), "an empty harp is no identity")
	assert.Error(t, Identity{Harp: "../escape"}.Validate())
}

// stagedReach is the spawn request for reach as the environment hands it to
// a runner: the credential moved into a secrets file at secretsPath, which
// read serves.
func stagedReach(t *testing.T, reach Endpoint, runID string) (env map[string]string, read func(string) ([]byte, error)) {
	t.Helper()
	const secretsPath = "/run/ctxloom/secrets/run.env"
	env = EncodeReach(reach, runID)
	require.Len(t, env, 3, "the reach-back trio and nothing else")
	file, err := EncodeSecrets(map[string]string{EnvCoordCred: env[EnvCoordCred]})
	require.NoError(t, err)
	delete(env, EnvCoordCred)
	env[EnvCoordCredFile] = secretsPath
	return env, func(name string) ([]byte, error) {
		if name == secretsPath {
			return file, nil
		}
		return nil, os.ErrNotExist
	}
}

func TestEncodeReach_DecodeReach_RoundTrip(t *testing.T) {
	reach := Endpoint{URL: "http://127.0.0.1:4321/mcp", Credential: "deadbeef"}
	env, read := stagedReach(t, reach, "run-7")

	got, runID, err := DecodeReach(lookup(env), read)
	require.NoError(t, err)
	assert.Equal(t, reach, got)
	assert.Equal(t, "run-7", runID)
}

// The credential is read from the secrets file only: an environment variable
// carrying it is not a credential.
func TestDecodeReach_ReadsTheCredentialOnlyFromTheSecretsFile(t *testing.T) {
	env := EncodeReach(Endpoint{URL: "http://127.0.0.1:4321/mcp", Credential: "deadbeef"}, "run-7")
	_, _, err := DecodeReach(lookup(env), noFiles)
	require.ErrorIs(t, err, ErrNoReachBack)

	_, _, err = DecodeReach(lookup(map[string]string{}), noFiles)
	require.ErrorIs(t, err, ErrNoReachBack, "an empty environment has no reach-back")
}

// noFiles is a readFile that holds nothing.
func noFiles(name string) ([]byte, error) { return nil, os.ErrNotExist }

// A named file that cannot be read, or that holds no credential, is no
// credential.
func TestDecodeReach_RefusesAnUnreadableOrEmptySecretsFile(t *testing.T) {
	env, _ := stagedReach(t, Endpoint{URL: "http://h:1/mcp", Credential: "deadbeef"}, "run-7")
	_, _, err := DecodeReach(lookup(env), noFiles)
	assert.ErrorIs(t, err, ErrNoReachBack)

	empty := func(string) ([]byte, error) { return []byte("OTHER=\"x\"\n"), nil }
	_, _, err = DecodeReach(lookup(env), empty)
	assert.ErrorIs(t, err, ErrNoReachBack)
}

func TestHookEnv_DecodeHookEnv_RoundTrip(t *testing.T) {
	id := Identity{Harp: "quiet-amber-falcon", Project: "proj-1"}
	env := HookEnv(id)
	require.Len(t, env, 2, "the two identity values hooks and taskloom key on")

	got, err := DecodeHookEnv(lookup(env))
	require.NoError(t, err)
	assert.Equal(t, id, got)
}

func TestDecodeHookEnv_RefusesAnInvalidHarp(t *testing.T) {
	_, err := DecodeHookEnv(lookup(map[string]string{}))
	require.Error(t, err, "no harp in the environment is no identity")
	_, err = DecodeHookEnv(lookup(map[string]string{EnvHarp: "../escape", EnvProjectID: "p"}))
	require.Error(t, err)
}

func TestEnvKeys_AreTheOnlySpellings(t *testing.T) {
	// The keys are read back by symbol everywhere else (the env-literals-once
	// gate); these pin the wire spellings the RUNNER and the ENGINE actually
	// receive, which cannot be renamed without a coordinated change.
	assert.Equal(t, "CTXLOOM_COORD_URL", EnvCoordURL)
	assert.Equal(t, "CTXLOOM_COORD_CRED", EnvCoordCred)
	assert.Equal(t, "CTXLOOM_RUN_ID", EnvRunID)
	assert.Equal(t, "CTXLOOM_SESSION_HARP", EnvHarp)
	assert.Equal(t, "CTXLOOM_PROJECT_ID", EnvProjectID)
}

func TestEncodeHookReach_DecodeHookReach_RoundTrip(t *testing.T) {
	hook := Endpoint{URL: "http://127.0.0.1:41234/hook", Credential: "bearer"}
	got, err := DecodeHookReach(lookup(EncodeHookReach(hook)))
	require.NoError(t, err)
	assert.Equal(t, hook, got)
}

func TestDecodeHookReach_RefusesAMissingBearer(t *testing.T) {
	env := EncodeHookReach(Endpoint{URL: "http://127.0.0.1:41234/hook", Credential: "bearer"})
	delete(env, EnvHookToken)
	_, err := DecodeHookReach(lookup(env))
	require.ErrorIs(t, err, ErrNoHookReach)
}

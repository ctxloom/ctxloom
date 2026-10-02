package sessions

import (
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

func TestEncodeReach_DecodeReach_RoundTrip(t *testing.T) {
	reach := Endpoint{URL: "http://127.0.0.1:4321/mcp", Credential: "deadbeef"}
	env := EncodeReach(reach, "run-7")
	require.Len(t, env, 3, "the reach-back trio and nothing else")

	got, runID, err := DecodeReach(lookup(env))
	require.NoError(t, err)
	assert.Equal(t, reach, got)
	assert.Equal(t, "run-7", runID)
}

func TestDecodeReach_RefusesAMissingCredential(t *testing.T) {
	env := EncodeReach(Endpoint{URL: "http://127.0.0.1:4321/mcp", Credential: "deadbeef"}, "run-7")
	delete(env, EnvCoordCred)
	_, _, err := DecodeReach(lookup(env))
	require.ErrorIs(t, err, ErrNoReachBack)

	_, _, err = DecodeReach(lookup(map[string]string{}))
	require.ErrorIs(t, err, ErrNoReachBack, "an empty environment has no reach-back")
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

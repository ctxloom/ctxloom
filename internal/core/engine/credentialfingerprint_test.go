package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCredentialFingerprint_TellsAFreshCredentialFromTheRefusedOne: the
// fingerprint changes exactly when a captured value does, never carries the
// value, and is "" when nothing was captured (a store read in place).
func TestCredentialFingerprint_TellsAFreshCredentialFromTheRefusedOne(t *testing.T) {
	dead := Credentials{Env: map[string]string{"TOKEN": "dead-value"}}
	fresh := Credentials{Env: map[string]string{"TOKEN": "fresh-value"}}

	assert.NotEmpty(t, dead.Fingerprint())
	assert.Equal(t, dead.Fingerprint(), Credentials{Env: map[string]string{"TOKEN": "dead-value"}}.Fingerprint(), "the same value, the same fingerprint")
	assert.NotEqual(t, dead.Fingerprint(), fresh.Fingerprint(), "a re-authenticated value is told apart")
	assert.NotContains(t, dead.Fingerprint(), "dead-value", "a fingerprint never carries the value")
	assert.NotEqual(t, dead.Fingerprint(), Credentials{Env: map[string]string{"OTHER": "dead-value"}}.Fingerprint(),
		"the carrier is part of it: one value under another variable is another credential")
	assert.Empty(t, Credentials{Stores: []SharedStore{{HomeRel: ".claude"}}}.Fingerprint(),
		"a store read in place has no value in hand to fingerprint")
}

// TestEnvFingerprint_IsTheFingerprintOfWhatALaunchWouldCapture: re-resolving
// a source's variables from an environment gives the fingerprint a launch
// from that environment would have recorded; an unset variable tells nothing.
func TestEnvFingerprint_IsTheFingerprintOfWhatALaunchWouldCapture(t *testing.T) {
	creds := Credentials{Env: map[string]string{"B": "2", "A": "1"}}
	env := map[string]string{"A": "1", "B": "2", "UNRELATED": "x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }

	assert.Equal(t, creds.Fingerprint(), EnvFingerprint([]string{"B", "A"}, lookup), "order is not identity")
	env["A"] = "refreshed"
	assert.NotEqual(t, creds.Fingerprint(), EnvFingerprint([]string{"A", "B"}, lookup))
	delete(env, "B")
	assert.Empty(t, EnvFingerprint([]string{"A", "B"}, lookup), "an unset carrier tells nothing")
	assert.Empty(t, EnvFingerprint(nil, lookup))
}

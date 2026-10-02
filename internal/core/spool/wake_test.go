package spool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A wake is a nonce ARMED on disk before anything is fired, and CONSUMED by
// the turn-start hook that the wake caused. The file is the only evidence a
// wake is in flight, so every test here reads the directory back.

func TestArmWake_WritesTheNonceBeforeAnythingFiresAndConsumeRedeemsItOnce(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()

	nonce, err := ArmWake(m, testHarp)
	require.NoError(t, err)
	require.NotEmpty(t, nonce)

	out, err := OutstandingWake(m, testHarp)
	require.NoError(t, err)
	assert.Equal(t, []string{nonce}, out, "an armed wake is outstanding until the hook redeems it")

	ok, err := ConsumeWake(m, testHarp, nonce)
	require.NoError(t, err)
	assert.True(t, ok, "the first redemption is the wake's acknowledgement")

	ok, err = ConsumeWake(m, testHarp, nonce)
	require.NoError(t, err)
	assert.False(t, ok, "a nonce redeems once: a second redemption is a stale wake")

	out, err = OutstandingWake(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, out)
}

func TestArmWake_MintsADistinctNonceEachTime(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	a, err := ArmWake(m, testHarp)
	require.NoError(t, err)
	b, err := ArmWake(m, testHarp)
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	out, err := OutstandingWake(m, testHarp)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{a, b}, out)
}

// The wake directory lives under in/, and nothing that reads in/ for MAIL may
// mistake an armed wake for a message: Pending must stay false and Claim must
// neither claim nor report it.
func TestArmWake_IsNotMail(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	_, err := ArmWake(m, testHarp)
	require.NoError(t, err)

	pending, err := Pending(m, testHarp)
	require.NoError(t, err)
	assert.False(t, pending, "an armed wake is not pending mail")

	res, err := Claim(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, res.Entries)
	assert.Empty(t, res.Problems, "an armed wake is not a malformed message either")
}

// The nonce reaches ConsumeWake from a PROMPT, i.e. from text anybody can
// type. It must never be able to name a path outside the wake directory.
func TestConsumeWake_RefusesANonceThatIsNotOneWeMint(t *testing.T) {
	root := hostHome(t)
	m := NewHomeMapper()
	victim := filepath.Join(root, "victim")
	require.NoError(t, os.WriteFile(victim, []byte("x"), 0o600))

	for _, bad := range []string{"", "../../../../victim", "UPPERCASEHEX0000", "abc"} {
		ok, err := ConsumeWake(m, testHarp, bad)
		assert.Error(t, err, "nonce %q", bad)
		assert.False(t, ok)
	}
	_, err := os.Stat(victim)
	assert.NoError(t, err, "a refused nonce removed nothing")
}

func TestConsumeWake_OnASpoolNeverCreatedIsNotAnError(t *testing.T) {
	hostHome(t)
	m := NewHomeMapper()
	nonce, err := ArmWake(m, "other-harp-never-used")
	require.NoError(t, err)
	ok, err := ConsumeWake(m, testHarp, nonce)
	require.NoError(t, err)
	assert.False(t, ok, "a nonce armed for another harp is not this harp's")
	out, err := OutstandingWake(m, testHarp)
	require.NoError(t, err)
	assert.Empty(t, out)
}

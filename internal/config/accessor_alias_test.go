package config

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U047-F02: cloneProfile once omitted Profile.DenyTools, so every copy-on-read
// profile accessor handed back a slice that aliased the shared Config's
// storage. The hole was closed as a side finding of U049-F05, but the gate that
// found it walks Fixture, not the accessors the review named — so nothing
// pinned the accessors themselves. These do.
//
// The contract under test is accessors.go's whole reason to exist: a value
// handed out by a Get* accessor is separately owned, and mutating it can never
// reach back into the Config every Load()/Current() holder shares.

// TestGetProfilesConfig_NeverAliasesConfigContainers is the class gate: it
// walks the returned value reflectively, so a Profile field added tomorrow and
// wired into cloneProfile without a clone helper fails HERE.
func TestGetProfilesConfig_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got := cfg.GetProfilesConfig()

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(got), "Config", "GetProfilesConfig")
}

// TestGetProfileDefinitions_NeverAliasesConfigContainers covers the second
// accessor over the same storage; the two build their result differently
// enough that one being right does not make the other right.
func TestGetProfileDefinitions_NeverAliasesConfigContainers(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())

	got := cfg.GetProfileDefinitions()

	assertNoSharedContainers(t, reflect.ValueOf(cfg).Elem(), reflect.ValueOf(got), "Config", "GetProfileDefinitions")
}

// TestGetProfileDefinitions_DenyToolsMutationDoesNotReachConfig states the
// instance F02 named, as behaviour rather than as pointer identity: a caller
// that appends to a returned deny-list must not be editing the shared config's
// deny-list. deny_tools gates which engine tools an agent may call, so an
// aliased slice is a permission surface mutated by action at a distance.
func TestGetProfileDefinitions_DenyToolsMutationDoesNotReachConfig(t *testing.T) {
	cfg := NewFixture(aliasProbeFixture())
	require.NotEmpty(t, cfg.GetProfileDefinitions()["p"].DenyTools,
		"the probe profile must carry a deny-list, or this pin proves nothing")

	got := cfg.GetProfileDefinitions()["p"]
	got.DenyTools[0] = "MUTATED"

	assert.Equal(t, []string{"Bash"}, cfg.GetProfileDefinitions()["p"].DenyTools,
		"GetProfileDefinitions must hand back an owned copy of DenyTools")
}

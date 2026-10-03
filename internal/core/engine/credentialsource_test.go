package engine

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

// secretValue stands in for a credential's value. It must never reach a
// CredentialSource: the source is journaled and shown to the human.
const secretValue = "sk-ant-oat01-THIS-IS-THE-SECRET"

func TestCredentialSource_NamesTheCarrierNeverTheValue(t *testing.T) {
	c := Credentials{
		Env:   map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": secretValue},
		Unset: []string{"ANTHROPIC_API_KEY"},
	}
	src := c.Source("claude-code")
	assert.NotContains(t, fmt.Sprintf("%#v", src), secretValue, "no part of the source may carry the value")
	assert.Equal(t, []string{"CLAUDE_CODE_OAUTH_TOKEN"}, src.EnvVars)
	assert.Empty(t, src.Stores)
	assert.NotEmpty(t, src.Key)

	other := Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "a-different-value"}}
	assert.Equal(t, src.Key, other.Source("claude-code").Key,
		"one coordinator resolves every run from one environment: the same carrier is the same credential")
}

func TestCredentialSource_DistinguishesCarrierEngineAndStore(t *testing.T) {
	token := Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("claude-code")
	apiKey := Credentials{Env: map[string]string{"ANTHROPIC_API_KEY": "v"}}.Source("claude-code")
	otherEngine := Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("other")
	login := Credentials{Stores: []SharedStore{{Var: "CLAUDE_CONFIG_DIR", HomeRel: ".claude"}}}.Source("claude-code")
	loginElsewhere := Credentials{Stores: []SharedStore{{Var: "CLAUDE_CONFIG_DIR", Value: "/srv/claude", HomeRel: ".claude"}}}.Source("claude-code")

	keys := map[string]string{"token": token.Key, "api-key": apiKey.Key, "other engine": otherEngine.Key, "login": login.Key, "login elsewhere": loginElsewhere.Key}
	seen := map[string]string{}
	for name, k := range keys {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s and %s share key %q", name, prev, k)
		}
		seen[k] = name
	}
	assert.Equal(t, []string{"~/.claude"}, login.Stores, "a default store is named where the engine finds it")
	assert.Equal(t, []string{"/srv/claude"}, loginElsewhere.Stores, "a store the env points at is named by that location")
	assert.Empty(t, login.EnvVars)
}

func TestCredentialSource_SortedSoOrderIsNotIdentity(t *testing.T) {
	// Eight names: Go randomizes map order, so an unsorted source matches the
	// sorted one by chance once in 40320 runs, not once in two.
	names := []string{"A", "B", "C", "D", "E", "F", "G", "H"}
	env := map[string]string{}
	for _, n := range names {
		env[n] = "v"
	}
	a := Credentials{Env: env}.Source("e")
	assert.Equal(t, names, a.EnvVars)
	stores := Credentials{Stores: []SharedStore{{Value: "/z"}, {Value: "/a"}}}.Source("e")
	assert.Equal(t, []string{"/a", "/z"}, stores.Stores)
}

// An engine that authenticates with nothing has no credential to share, and
// so nothing to be parked WITH: its key is empty.
func TestCredentialSource_NoCredentialIsNoSource(t *testing.T) {
	assert.Equal(t, "", Credentials{}.Source("mock").Key)
	assert.Equal(t, "", Credentials{Unset: []string{"X"}}.Source("mock").Key, "unsetting others is not a credential")
}

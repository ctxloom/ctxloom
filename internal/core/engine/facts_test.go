package engine

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// fakeAuth is an Auth with the modes a test names and nothing else.
type fakeAuth struct{ modes []AuthMode }

func (f fakeAuth) Modes() []AuthMode { return f.modes }
func (fakeAuth) Credentials(AuthMode, func(string) (string, bool)) (Credentials, error) {
	return Credentials{}, nil
}

func validHome() HomeSpec {
	return HomeSpec{
		Vars: []HomeVar{{Name: "X_CONFIG_DIR", Subdir: "x"}},
		Auth: Provide[Auth](fakeAuth{modes: []AuthMode{AuthLogin, AuthToken}}),
	}
}

// TestHomeSpec_ZeroValue_IsTheNullObject: an engine that keeps no home
// returns the zero spec, and it validates — nothing is relocated and no
// Declared slot has to be written for it.
func TestHomeSpec_ZeroValue_IsTheNullObject(t *testing.T) {
	var zero HomeSpec
	require.NoError(t, zero.Validate())
	assert.False(t, zero.Relocates())
	_, ok := zero.Auth.Get()
	assert.False(t, ok)
}

// Auth on a spec that relocates nothing is refused: the auth check guards a
// relocated home, and there is none.
func TestHomeSpec_Validate_RefusesAuthWithNoVar(t *testing.T) {
	h := HomeSpec{Auth: Provide[Auth](fakeAuth{modes: []AuthMode{AuthToken}})}
	assert.ErrorContains(t, h.Validate(), "no home var")
}

func TestHomeSpec_Validate_AcceptsACompleteDeclaration(t *testing.T) {
	require.NoError(t, validHome().Validate())
}

func TestHomeSpec_Validate_RefusesEmptyVarFields(t *testing.T) {
	h := validHome()
	h.Vars[0].Subdir = ""
	assert.ErrorContains(t, h.Validate(), "Subdir")
	h = validHome()
	h.Vars[0].Name = ""
	assert.ErrorContains(t, h.Validate(), "Name")
}

// An undecided Auth slot is the omission the type family exists to refuse:
// an engine with a relocatable home MUST say how a run there authenticates.
func TestHomeSpec_Validate_RefusesUndecidedAuth(t *testing.T) {
	h := validHome()
	h.Auth = Declared[Auth]{}
	assert.ErrorContains(t, h.Validate(), "Auth")
}

func TestHomeSpec_Validate_AcceptsAbsentAuth(t *testing.T) {
	h := validHome()
	h.Auth = Absent[Auth]("authenticates against no vendor")
	assert.NoError(t, h.Validate())
}

// An Auth a binding could never select is refused at registration: one with
// no mode, a nil one, and one naming a mode outside the shared vocabulary.
func TestHomeSpec_Validate_RefusesAnUnselectableAuth(t *testing.T) {
	h := validHome()
	h.Auth = Provide[Auth](fakeAuth{})
	assert.ErrorContains(t, h.Validate(), "no mode")
	h.Auth = Provide[Auth](nil)
	assert.ErrorContains(t, h.Validate(), "nil")
	h.Auth = Provide[Auth](fakeAuth{modes: []AuthMode{"keychain"}})
	assert.ErrorContains(t, h.Validate(), `"keychain"`)
}

// Undeclared auth is the token: the default never reaches the human's own
// login. The vocabulary is the token every agent runs on and the login the
// human's own session may share; anything else -- the retired api-key and
// cloud among them -- is refused, typed, never defaulted.
func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]AuthMode{"": AuthToken, " login ": AuthLogin, "token": AuthToken} {
		got, err := ParseAuthMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	assert.Equal(t, []string{"login", "token"}, AuthModeNames())
	for _, in := range []string{"api-key", "cloud", "apikey"} {
		_, err := ParseAuthMode(in)
		require.ErrorIs(t, err, ErrUnknownAuthMode, in)
		var r report.Remediable
		require.ErrorAs(t, err, &r)
		for _, m := range AuthModeNames() {
			assert.Contains(t, r.Remedy(), m)
		}
	}
}

// Every run ctxloom spawns authenticates with the token, so an engine that
// declares auth at all must offer it.
func TestHomeSpec_AnAuthWithoutTheTokenIsRefused(t *testing.T) {
	h := validHome()
	h.Auth = Provide[Auth](fakeAuth{modes: []AuthMode{AuthLogin}})
	assert.ErrorContains(t, h.Validate(), "token")
	h.Auth = Provide[Auth](fakeAuth{modes: []AuthMode{AuthToken}})
	assert.NoError(t, h.Validate())
}

func TestSupportsMode(t *testing.T) {
	a := fakeAuth{modes: []AuthMode{AuthToken}}
	assert.True(t, SupportsMode(a, AuthToken))
	assert.False(t, SupportsMode(a, AuthLogin))
}

func validContainer() ContainerSpec {
	return ContainerSpec{
		Install:         []byte("RUN true\n"),
		ValidateCommand: "x --version",
		OverlayDirs:     []string{".x"},
	}
}

func TestContainerSpec_Validate_AcceptsACompleteDeclaration(t *testing.T) {
	require.NoError(t, validContainer().Validate())
}

func TestContainerSpec_Validate_RefusesInstallWithoutValidate(t *testing.T) {
	c := validContainer()
	c.ValidateCommand = ""
	assert.ErrorContains(t, c.Validate(), "ValidateCommand")
}

// TranscriptStoreRel is followed inside a Linux container as well as on the
// host, so it is a clean relative slash path below the session home whatever
// the host separator: a filepath-built value carries `\` on a Windows host.
func TestHomeSpec_Validate_RefusesANonSlashTranscriptStore(t *testing.T) {
	for _, rel := range []string{`x\projects`, "/root/projects", "../projects", "x//projects", "projects/"} {
		h := validHome()
		h.TranscriptStoreRel = rel
		assert.ErrorContains(t, h.Validate(), "TranscriptStoreRel", rel)
	}
	h := validHome()
	h.TranscriptStoreRel = "projects"
	assert.NoError(t, h.Validate())
	h.TranscriptStoreRel = ""
	assert.NoError(t, h.Validate(), "an engine that keeps no history declares none")
}

// A history store is relative to a session home, so an engine that relocates
// nothing cannot declare one.
func TestHomeSpec_Validate_RefusesATranscriptStoreWithNoVar(t *testing.T) {
	assert.ErrorContains(t, HomeSpec{TranscriptStoreRel: "projects"}.Validate(), "TranscriptStoreRel")
}

// HostDir is where a shared store lives on the host: the launching env's own
// value when it names one, else the store's place under the home; a store
// that is no directory (an OS keychain) has none.
func TestSharedStore_HostDir(t *testing.T) {
	home := filepath.Join("h", "ben")
	assert.Equal(t, filepath.Join(home, ".claude"), SharedStore{Var: "V", HomeRel: ".claude"}.HostDir(home), "an empty Value is the engine's default")
	assert.Equal(t, "/elsewhere", SharedStore{Var: "V", Value: "/elsewhere", HomeRel: ".claude"}.HostDir(home), "the launching env's value wins")
	assert.Equal(t, filepath.Join(home, ".config", "gcloud"), SharedStore{HomeRel: ".config/gcloud"}.HostDir(home), "HomeRel is slash-separated")
	assert.Equal(t, "", SharedStore{Var: "V"}.HostDir(home), "a keychain-backed store has no directory")
	assert.Equal(t, "", SharedStore{Value: "/ignored"}.HostDir(home), "a Value with no Var names nothing")
}

// An answer may be recorded under a directory's own name (a claude beside
// this process wrote it) or under its host name (the human's claude on the
// host did): Keys names both, once each, and only what it can name.
func TestTrustQuery_KeysNameTheViewAndTheHost(t *testing.T) {
	prefixed := func(d string) (string, error) { return "/host" + d, nil }
	unnamed := func(string) (string, error) { return "", errors.New("no host name") }
	for name, tc := range map[string]struct {
		hostPath func(string) (string, error)
		want     []string
	}{
		"same paths as the host":  {want: []string{"/w"}},
		"identity host name":      {hostPath: func(d string) (string, error) { return d, nil }, want: []string{"/w"}},
		"named elsewhere on host": {hostPath: prefixed, want: []string{"/w", "/host/w"}},
		"no host name":            {hostPath: unnamed, want: []string{"/w"}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, TrustQuery{HostPath: tc.hostPath}.Keys("/w"))
		})
	}
}

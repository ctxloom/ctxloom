package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// fakeAuth is an Auth with the modes a test names and nothing else.
type fakeAuth struct{ modes []AuthMode }

func (f fakeAuth) Modes() []AuthMode { return f.modes }
func (fakeAuth) Credentials(AuthMode, func(string) (string, bool), CredentialReader) (Credentials, error) {
	return Credentials{}, nil
}
func (fakeAuth) Mint(context.Context, AuthMode, Terminal) ([]byte, error) {
	return nil, ErrMintUnsupported
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
// login. An unknown spelling is refused, typed, never defaulted.
func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]AuthMode{"": AuthToken, " login ": AuthLogin, "token": AuthToken, "api-key": AuthAPIKey, "cloud": AuthCloud} {
		got, err := ParseAuthMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := ParseAuthMode("apikey")
	require.ErrorIs(t, err, ErrUnknownAuthMode)
	var r report.Remediable
	require.ErrorAs(t, err, &r)
	for _, m := range AuthModeNames() {
		assert.Contains(t, r.Remedy(), m)
	}
}

// Only a token and an API key are ctxloom's to store; only the token is the
// engine's to mint.
func TestAuthMode_StoredAndMinted(t *testing.T) {
	for m, want := range map[AuthMode][2]bool{AuthLogin: {false, false}, AuthToken: {true, true}, AuthAPIKey: {true, false}, AuthCloud: {false, false}} {
		assert.Equal(t, want[0], m.Stored(), "Stored %s", m)
		assert.Equal(t, want[1], m.Minted(), "Minted %s", m)
	}
}

// CheckAuth is the one check of an auth selection. Every invalid selection
// is refused with its own sentinel and a remedy; where the fix is another
// mode, the remedy names exactly the modes THIS engine supports, read from
// its Modes().
func TestCheckAuth_EveryInvalidSelectionIsTypedWithARemedy(t *testing.T) {
	withAuth := Provide[Auth](fakeAuth{modes: []AuthMode{AuthToken, AuthCloud}})
	noAuth := Absent[Auth]("authenticates against no vendor")
	for _, tc := range []struct {
		name     string
		declared Declared[Auth]
		mode     string
		sentinel error
		modes    bool
	}{
		{"an unknown spelling", withAuth, "apikey", ErrUnknownAuthMode, true},
		{"a mode the engine lacks", withAuth, "login", ErrAuthModeUnsupported, true},
		{"undeclared, and the engine lacks the token default", Provide[Auth](fakeAuth{modes: []AuthMode{AuthCloud}}), "", ErrAuthModeUnsupported, false},
		{"any mode on an engine with no auth", noAuth, "token", ErrEngineHasNoAuth, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CheckAuth("x", tc.declared, tc.mode)
			require.ErrorIs(t, err, tc.sentinel)
			var r report.Remediable
			require.ErrorAs(t, err, &r)
			require.NotEmpty(t, r.Remedy())
			if a, ok := tc.declared.Get(); ok {
				for _, m := range a.Modes() {
					assert.Contains(t, r.Remedy(), string(m), "the remedy names the engine's own modes")
				}
			}
		})
	}
}

func TestCheckAuth_ValidSelections(t *testing.T) {
	withAuth := Provide[Auth](fakeAuth{modes: []AuthMode{AuthToken, AuthCloud}})
	m, err := CheckAuth("x", withAuth, "")
	require.NoError(t, err)
	assert.Equal(t, AuthToken, m, "undeclared is the token")
	m, err = CheckAuth("x", withAuth, " cloud ")
	require.NoError(t, err)
	assert.Equal(t, AuthCloud, m)
	m, err = CheckAuth("x", Absent[Auth]("none"), "")
	require.NoError(t, err)
	assert.Empty(t, m, "an engine with no auth and no declaration has nothing to check")
}

func TestSupportsMode(t *testing.T) {
	a := fakeAuth{modes: []AuthMode{AuthToken}}
	assert.True(t, SupportsMode(a, AuthToken))
	assert.False(t, SupportsMode(a, AuthAPIKey))
}

func validContainer() ContainerSpec {
	return ContainerSpec{
		Install:            []byte("RUN true\n"),
		ValidateCommand:    "x --version",
		OverlayDirs:        []string{".x"},
		TranscriptStoreRel: ".x/projects",
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

// HostDir is where a shared store lives on the host: the launching env's own
// value when it names one, else the store's place under the home; a store
// that is no directory (an OS keychain) has none.
func TestSharedStore_HostDir(t *testing.T) {
	home := filepath.Join("h", "ben")
	assert.Equal(t, filepath.Join(home, ".claude"), SharedStore{Var: "V", HomeRel: ".claude"}.HostDir(home), "an empty Value is the engine's default")
	assert.Equal(t, "/elsewhere", SharedStore{Var: "V", Value: "/elsewhere", HomeRel: ".claude"}.HostDir(home), "the launching env's value wins")
	assert.Equal(t, filepath.Join(home, ".config", "gcloud"), SharedStore{HomeRel: ".config/gcloud", ReadOnly: true}.HostDir(home), "HomeRel is slash-separated")
	assert.Equal(t, "", SharedStore{Var: "V"}.HostDir(home), "a keychain-backed store has no directory")
	assert.Equal(t, "", SharedStore{Value: "/ignored"}.HostDir(home), "a Value with no Var names nothing")
}

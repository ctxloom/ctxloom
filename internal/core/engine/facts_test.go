package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuth is an Auth with the modes a test names and nothing else.
type fakeAuth struct{ modes []AuthMode }

func (f fakeAuth) Modes() []AuthMode { return f.modes }
func (fakeAuth) LaunchEnv(AuthMode, func(string) (string, bool), CredentialReader) (map[string]string, error) {
	return nil, nil
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
// login. An unknown spelling is refused, never defaulted.
func TestParseAuthMode(t *testing.T) {
	for in, want := range map[string]AuthMode{"": AuthToken, " login ": AuthLogin, "token": AuthToken, "api-key": AuthAPIKey} {
		got, err := ParseAuthMode(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	_, err := ParseAuthMode("apikey")
	assert.ErrorContains(t, err, "login, token, api-key")
}

// The login is the human's and is never stored by ctxloom; every other mode's
// credential is.
func TestAuthMode_Stored(t *testing.T) {
	assert.False(t, AuthLogin.Stored())
	assert.True(t, AuthToken.Stored())
	assert.True(t, AuthAPIKey.Stored())
}

func TestSupportsMode(t *testing.T) {
	a := fakeAuth{modes: []AuthMode{AuthToken}}
	assert.True(t, SupportsMode(a, AuthToken))
	assert.False(t, SupportsMode(a, AuthAPIKey))
}

func validContainer() ContainerSpec {
	return ContainerSpec{
		Install:         []byte("RUN true\n"),
		ValidateCommand: "x --version",
		Auth: Provide(ContainerAuth{
			EnvTriggers:    []string{"X_API_KEY"},
			EnvPassthrough: []string{"X_API_KEY", "X_BASE_URL"},
			Hint:           "no X_API_KEY",
			Remedy:         "export X_API_KEY",
		}),
		OverlayDirs:        []string{".x"},
		TranscriptStoreRel: ".x/projects",
	}
}

func TestContainerSpec_Validate_AcceptsACompleteDeclaration(t *testing.T) {
	require.NoError(t, validContainer().Validate())
}

func TestContainerSpec_Validate_RefusesUndecidedAuth(t *testing.T) {
	c := validContainer()
	c.Auth = Declared[ContainerAuth]{}
	assert.ErrorContains(t, c.Validate(), "Auth")
}

func TestContainerSpec_Validate_RefusesInstallWithoutValidate(t *testing.T) {
	c := validContainer()
	c.ValidateCommand = ""
	assert.ErrorContains(t, c.Validate(), "ValidateCommand")
}

// A vendorless auth (mock: authenticates against nothing) must not ALSO name
// triggers: the two shapes mean opposite things at resolve time and a
// declaration carrying both has no single reading.
func TestContainerAuth_Validate_VendorlessExcludesTriggers(t *testing.T) {
	a := ContainerAuth{Vendorless: "authenticates against no vendor", EnvTriggers: []string{"X"}}
	assert.ErrorContains(t, a.Validate(), "Vendorless")
	a = ContainerAuth{Vendorless: "authenticates against no vendor"}
	assert.NoError(t, a.Validate())
}

// A vendor-backed auth with no trigger can never resolve — every containerized run of it would degrade. That is a
// declaration of "unknown", which is what Absent is for.
func TestContainerAuth_Validate_RefusesNothingToResolve(t *testing.T) {
	a := ContainerAuth{Hint: "no way in"}
	assert.ErrorContains(t, a.Validate(), "resolve")
}

func TestContainerAuth_Validate_RefusesMissingRemedy(t *testing.T) {
	a := ContainerAuth{EnvTriggers: []string{"X_API_KEY"}, Hint: "no X_API_KEY"}
	assert.ErrorContains(t, a.Validate(), "Remedy")
}

func TestContainerAuth_Validate_RefusesMissingHint(t *testing.T) {
	a := ContainerAuth{EnvTriggers: []string{"X_API_KEY"}}
	assert.ErrorContains(t, a.Validate(), "Hint")
}

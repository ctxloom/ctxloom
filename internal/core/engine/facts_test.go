package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHome() HomeSpec {
	return HomeSpec{
		Vars:        []HomeVar{{Name: "X_CONFIG_DIR", Subdir: "x"}},
		Auth:        Provide(TokenAuth{TokenVar: "X_TOKEN", EnvTriggers: []string{"X_API_KEY"}, MintHint: "x setup-token"}),
		SharedLogin: Provide(SharedLogin{Var: "X_STORAGE_DIR", FallbackVar: "X_CONFIG_DIR"}),
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

// Token auth on a spec that relocates nothing is refused: the auth check
// guards a relocated home, and there is none.
func TestHomeSpec_Validate_RefusesAuthWithNoVar(t *testing.T) {
	h := HomeSpec{Auth: Provide(TokenAuth{TokenVar: "X_TOKEN", MintHint: "x setup-token"})}
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
	h.Auth = Declared[TokenAuth]{}
	assert.ErrorContains(t, h.Validate(), "Auth")
}

func TestHomeSpec_Validate_AcceptsAbsentAuth(t *testing.T) {
	h := validHome()
	h.Auth = Absent[TokenAuth]("authenticates against no vendor")
	assert.NoError(t, h.Validate())
}

// A token-auth declaration must name the var the engine reads its token from
// and the command that mints one: the refusal of an unauthenticated run
// names both.
func TestHomeSpec_Validate_TokenAuthNamesItsVarAndItsMintCommand(t *testing.T) {
	h := validHome()
	h.Auth = Provide(TokenAuth{MintHint: "x setup-token"})
	assert.ErrorContains(t, h.Validate(), "TokenVar")
	h.Auth = Provide(TokenAuth{TokenVar: "X_TOKEN"})
	assert.ErrorContains(t, h.Validate(), "MintHint")
}

// An undecided SharedLogin is refused like an undecided Auth: an engine with
// a relocatable home must say whether a host run can share the human's login.
func TestHomeSpec_Validate_RefusesUndecidedSharedLogin(t *testing.T) {
	h := validHome()
	h.SharedLogin = Declared[SharedLogin]{}
	assert.ErrorContains(t, h.Validate(), "SharedLogin")
	h.SharedLogin = Absent[SharedLogin]("keeps no credential storage of its own")
	assert.NoError(t, h.Validate())
}

func TestHomeSpec_Validate_SharedLoginNamesBothVars(t *testing.T) {
	h := validHome()
	h.SharedLogin = Provide(SharedLogin{FallbackVar: "X_CONFIG_DIR"})
	assert.ErrorContains(t, h.Validate(), "Var is empty")
	h.SharedLogin = Provide(SharedLogin{Var: "X_STORAGE_DIR"})
	assert.ErrorContains(t, h.Validate(), "FallbackVar")
}

func TestHomeSpec_Validate_RefusesSharedLoginWithNoVar(t *testing.T) {
	h := HomeSpec{SharedLogin: Provide(SharedLogin{Var: "X_STORAGE_DIR", FallbackVar: "X_CONFIG_DIR"})}
	assert.ErrorContains(t, h.Validate(), "no home var")
}

// Value is the string the launching env's own engine resolves its storage
// from, byte for byte: the storage var when set (even to ""), else the
// fallback's value, else "".
func TestSharedLogin_Value_IsWhatTheLaunchingEnvResolves(t *testing.T) {
	l := SharedLogin{Var: "X_STORAGE_DIR", FallbackVar: "X_CONFIG_DIR"}
	env := func(kv map[string]string) func(string) (string, bool) {
		return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"neither set is the engine's default", map[string]string{}, ""},
		{"the fallback verbatim, never cleaned", map[string]string{"X_CONFIG_DIR": "/h/./cfg/"}, "/h/./cfg/"},
		{"an inherited storage var wins over the fallback", map[string]string{"X_STORAGE_DIR": "/real", "X_CONFIG_DIR": "/session"}, "/real"},
		{"an inherited empty storage var is kept", map[string]string{"X_STORAGE_DIR": "", "X_CONFIG_DIR": "/session"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, l.Value(env(tc.env)))
		})
	}
}

func validContainer() ContainerSpec {
	return ContainerSpec{
		Install:         []byte("RUN true\n"),
		ValidateCommand: "x --version",
		Auth: Provide(ContainerAuth{
			EnvTriggers:    []string{"X_API_KEY"},
			EnvPassthrough: []string{"X_API_KEY", "X_BASE_URL"},
			Hint:           "no X_API_KEY",
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

func TestContainerAuth_Validate_RefusesMissingHint(t *testing.T) {
	a := ContainerAuth{EnvTriggers: []string{"X_API_KEY"}}
	assert.ErrorContains(t, a.Validate(), "Hint")
}

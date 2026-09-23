package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validHome() HomeSpec {
	return HomeSpec{
		Vars: []HomeVar{{Name: "X_CONFIG_DIR", Subdir: "x"}},
		Auth: Provide(TokenAuth{TokenVar: "X_TOKEN", EnvTriggers: []string{"X_API_KEY"}, MintHint: "x setup-token"}),
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
